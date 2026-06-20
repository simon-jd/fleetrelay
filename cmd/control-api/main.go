package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/fleetrelay/fleetrelay/internal/control"
	"github.com/fleetrelay/fleetrelay/internal/control/memory"
	"github.com/fleetrelay/fleetrelay/internal/control/postgres"
	"github.com/fleetrelay/fleetrelay/internal/controlhttp"
	"github.com/redis/go-redis/v9"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx := context.Background()
	var store control.Store
	var closeFn = func() {}
	var presence control.Presence
	var rdb *redis.Client
	if u := os.Getenv("DATABASE_URL"); u != "" {
		p, e := postgres.Open(ctx, u)
		if e != nil {
			log.Error("postgres unavailable", "error", e)
			os.Exit(1)
		}
		store = p
		closeFn = p.Close
	} else {
		log.Warn("using in-memory store; state is not durable")
		store = memory.New()
	}
	if redisAddr := os.Getenv("REDIS_ADDR"); redisAddr != "" {
		rdb = redis.NewClient(&redis.Options{Addr: redisAddr})
		defer rdb.Close()
		presence = func(ctx context.Context, deviceID string) (bool, error) {
			raw, err := rdb.Get(ctx, "relay:presence:"+deviceID).Bytes()
			if errors.Is(err, redis.Nil) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			var owner struct {
				GatewayID       string `json:"gateway_id"`
				GatewayInstance string `json:"gateway_instance"`
			}
			if err = json.Unmarshal(raw, &owner); err != nil {
				return false, err
			}
			if owner.GatewayID == "" || owner.GatewayInstance == "" {
				return false, nil
			}
			instance, err := rdb.Get(ctx, "relay:gateway:"+owner.GatewayID+":alive").Result()
			if errors.Is(err, redis.Nil) {
				return false, nil
			}
			return instance == owner.GatewayInstance, err
		}
	} else if os.Getenv("DATABASE_URL") != "" {
		log.Error("REDIS_ADDR is required with durable mode to enforce REQUIRE_ONLINE")
		os.Exit(1)
	}
	defer closeFn()
	svc := control.NewService(store, presence, 30*time.Second)
	ready := func(ctx context.Context) error {
		if err := store.Ping(ctx); err != nil {
			return err
		}
		if rdb != nil {
			return rdb.Ping(ctx).Err()
		}
		return nil
	}
	addr := env("HTTP_ADDR", ":8080")
	log.Info("control-api listening", "addr", addr)
	if e := http.ListenAndServe(addr, controlhttp.Server{Service: svc, Store: store, InternalToken: env("INTERNAL_SHARED_TOKEN", "dev-internal-only"), DevOperatorToken: env("DEV_OPERATOR_TOKEN", "dev-operator-token"), DevViewerToken: env("DEV_VIEWER_TOKEN", "dev-viewer-token"), Ready: ready}.Handler()); e != nil {
		log.Error("server stopped", "error", e)
		os.Exit(1)
	}
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
