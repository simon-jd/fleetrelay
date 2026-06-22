package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/fleetrelay/fleetrelay/internal/control"
	"github.com/fleetrelay/fleetrelay/internal/control/postgres"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

type presence struct {
	GatewayID       string `json:"gateway_id"`
	GatewayInstance string `json:"gateway_instance"`
	SessionID       string `json:"session_id"`
	Generation      uint64 `json:"generation"`
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	db, e := postgres.Open(ctx, required("DATABASE_URL"))
	if e != nil {
		log.Error("postgres unavailable", "error", e)
		os.Exit(1)
	}
	defer db.Close()
	releaseLeadership, checkLeadership, leader, e := db.TryDispatcherLeadership(ctx)
	if e != nil {
		log.Error("acquire dispatcher leadership", "error", e)
		os.Exit(1)
	}
	if !leader {
		log.Error("another dispatcher is already active")
		os.Exit(1)
	}
	leadershipDone := make(chan struct{})
	go func() {
		defer close(leadershipDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkCtx, stop := context.WithTimeout(ctx, 2*time.Second)
				err := checkLeadership(checkCtx)
				stop()
				if err != nil {
					log.Error("dispatcher leadership lost; stopping", "error", err)
					cancel()
					return
				}
			}
		}
	}()
	defer func() {
		cancel()
		<-leadershipDone
		releaseLeadership()
	}()
	rdb := redis.NewClient(&redis.Options{Addr: env("REDIS_ADDR", "redis:6379")})
	srv := &http.Server{Addr: env("METRICS_ADDR", ":8082"), Handler: dispatcherHTTP(db, rdb)}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("dispatcher HTTP stopped", "error", err)
			cancel()
		}
	}()
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		srv.Shutdown(shutdown)
	}()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	retryTicker := time.NewTicker(time.Second)
	defer retryTicker.Stop()
	maxAttempts := uint32(envInt("MAX_DELIVERY_ATTEMPTS", 3))
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			batchCtx, stop := context.WithTimeout(ctx, 15*time.Second)
			items, e := db.ClaimOutbox(batchCtx, 64)
			if e != nil {
				log.Warn("claim outbox", "error", e)
				stop()
				continue
			}
			for _, item := range items {
				if e = publish(batchCtx, db, rdb, item); e != nil {
					log.Warn("publish outbox", "outbox_id", item.ID, "error", e)
					continue
				}
				if e = db.MarkOutboxPublished(batchCtx, item.ID, time.Now()); e != nil && !errors.Is(e, control.ErrNotFound) {
					log.Warn("mark published", "error", e)
				}
			}
			stop()
		case <-retryTicker.C:
			batchCtx, stop := context.WithTimeout(ctx, 15*time.Second)
			retryDeadlines(batchCtx, db, rdb, log, maxAttempts)
			stop()
		}
	}
}
func dispatcherHTTP(db *postgres.Store, rdb *redis.Client) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"status":"ok"}`)) })
	m.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if db.Ping(r.Context()) != nil || rdb.Ping(r.Context()).Err() != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		w.Write([]byte(`{"status":"ok"}`))
	})
	m.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.Write([]byte("fleetrelay_up 1\n"))
	})
	return m
}
func publish(ctx context.Context, db *postgres.Store, rdb *redis.Client, item control.OutboxItem) error {
	if item.Kind != "command.created" {
		return rdb.XAdd(ctx, &redis.XAddArgs{Stream: "relay:fleet:events", MaxLen: 10000, Approx: true, Values: map[string]any{"event_id": item.ID, "kind": item.Kind, "payload": string(item.Payload)}}).Err()
	}
	c, handled, e := db.PrepareOutboxCommand(ctx, item.AggregateID, time.Now())
	if e != nil {
		return e
	}
	if handled {
		return nil
	}
	raw, e := rdb.Get(ctx, "relay:presence:"+c.DeviceID).Bytes()
	if e != nil {
		return e
	}
	var p presence
	if e = json.Unmarshal(raw, &p); e != nil {
		return e
	}
	if e = requireGatewayAlive(ctx, rdb, p.GatewayID, p.GatewayInstance); e != nil {
		return e
	}
	if _, e = db.CreateAttempt(ctx, c.ID, p.GatewayID, p.Generation, time.Now(), time.Now().Add(3*time.Second)); e != nil {
		return e
	}
	return rdb.XAdd(ctx, &redis.XAddArgs{Stream: "relay:gateway:" + p.GatewayID + ":commands", Values: map[string]any{"command_id": c.ID, "command": string(item.Payload), "generation": p.Generation}}).Err()
}
func retryDeadlines(ctx context.Context, db *postgres.Store, rdb *redis.Client, log *slog.Logger, max uint32) {
	items, err := db.ListRetryCandidates(ctx, time.Now(), 64)
	if err != nil {
		log.Warn("list retry candidates", "error", err)
		return
	}
	for _, item := range items {
		_, changed, e := db.ResolveAttemptDeadline(ctx, item.Command.ID, max, time.Now())
		if e != nil {
			log.Warn("resolve attempt deadline", "command_id", item.Command.ID, "error", e)
			continue
		}
		if changed || item.AttemptCount >= max {
			continue
		}
		raw, e := rdb.Get(ctx, "relay:presence:"+item.Command.DeviceID).Bytes()
		if e != nil {
			log.Warn("retry presence unavailable", "command_id", item.Command.ID, "error", e)
			continue
		}
		var p presence
		if e = json.Unmarshal(raw, &p); e != nil {
			log.Warn("retry presence invalid", "error", e)
			continue
		}
		if e = requireGatewayAlive(ctx, rdb, p.GatewayID, p.GatewayInstance); e != nil {
			log.Debug("retry gateway not live", "command_id", item.Command.ID, "gateway_id", p.GatewayID)
			continue
		}
		now := time.Now()
		if _, e = db.CreateAttempt(ctx, item.Command.ID, p.GatewayID, p.Generation, now, now.Add(3*time.Second)); e != nil {
			log.Warn("create retry attempt", "command_id", item.Command.ID, "error", e)
			continue
		}
		payload, _ := json.Marshal(item.Command)
		if e = rdb.XAdd(ctx, &redis.XAddArgs{Stream: "relay:gateway:" + p.GatewayID + ":commands", Values: map[string]any{"command_id": item.Command.ID, "command": string(payload), "generation": p.Generation}}).Err(); e != nil {
			log.Warn("publish retry attempt", "command_id", item.Command.ID, "error", e)
		}
	}
}

func requireGatewayAlive(ctx context.Context, rdb *redis.Client, gatewayID, expectedInstance string) error {
	if gatewayID == "" || expectedInstance == "" {
		return control.ErrUnavailable
	}
	instance, err := rdb.Get(ctx, "relay:gateway:"+gatewayID+":alive").Result()
	if errors.Is(err, redis.Nil) {
		return control.ErrUnavailable
	}
	if err != nil {
		return err
	}
	if instance != expectedInstance {
		return control.ErrUnavailable
	}
	return nil
}
func required(k string) string {
	v := os.Getenv(k)
	if v == "" {
		panic(k + " is required")
	}
	return v
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func envInt(k string, d int) int {
	v, e := strconv.Atoi(env(k, strconv.Itoa(d)))
	if e != nil || v < 1 {
		return d
	}
	return v
}
