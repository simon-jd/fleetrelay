package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/fleetrelay/fleetrelay/internal/control"
	"github.com/fleetrelay/fleetrelay/internal/testutil"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestOutboxBatchPublishesNewCommittedIntentWithoutAnotherTick(t *testing.T) {
	db, pool, _ := testutil.Postgres(t)
	ctx := t.Context()
	now := time.Now().UTC()
	c, _ := testutil.Command(t, db, now)
	gateway := "followup-" + uuid.NewString()
	sess, err := db.RegisterSession(ctx, control.Device{ID: c.DeviceID, AdapterType: "sim"}, gateway, now)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(dispatcherRedisOptions(testutil.RequiredEnv(t, "FLEETRELAY_TEST_REDIS_ADDR")))
	defer rdb.Close()
	stream := "relay:gateway:" + gateway + ":commands"
	presenceKey := "relay:presence:" + c.DeviceID
	aliveKey := "relay:gateway:" + gateway + ":alive"
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		client := redis.NewClient(dispatcherRedisOptions(testutil.RequiredEnv(t, "FLEETRELAY_TEST_REDIS_ADDR")))
		defer client.Close()
		if err := client.Del(cleanup, stream, presenceKey, aliveKey).Err(); err != nil {
			t.Error(err)
		}
	})
	p, _ := json.Marshal(presence{GatewayID: gateway, GatewayInstance: "instance-1", Generation: sess.Generation})
	if err := rdb.Set(ctx, presenceKey, p, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, aliveKey, "instance-1", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := processOutboxBatch(ctx, db, rdb, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	messages, err := rdb.XRange(ctx, stream, "-", "+").Result()
	if err != nil || len(messages) != 1 {
		t.Fatalf("one batch must publish the new durable intent: messages=%d error=%v", len(messages), err)
	}
	var intentID string
	var handled bool
	if err := pool.QueryRow(ctx, `SELECT outbox_id::text,published_at IS NOT NULL FROM outbox WHERE aggregate_id=$1 AND kind='command.delivery'`, c.ID).Scan(&intentID, &handled); err != nil {
		t.Fatal(err)
	}
	if !handled || messages[0].Values["delivery_id"] != intentID || messages[0].Values["command_id"] != c.ID || messages[0].Values["attempt"] != "1" {
		t.Fatal("published message does not match the committed intent")
	}
}

func TestOutboxBatchMakesProgressPastPoisonRows(t *testing.T) {
	db, pool, _ := testutil.Postgres(t)
	ctx := t.Context()
	rdb := redis.NewClient(&redis.Options{Addr: testutil.RequiredEnv(t, "FLEETRELAY_TEST_REDIS_ADDR"), MaxRetries: -1})
	defer rdb.Close()
	// Valid JSONB of the wrong Go type models corrupted persisted envelopes.
	// Older poison rows fill exactly one production polling batch.
	_, err := pool.Exec(ctx, `INSERT INTO outbox(aggregate_id,kind,payload,business_key,created_at)
		SELECT 'poison-'||n,'command.delivery','"invalid-delivery"'::jsonb,'poison-'||n,now()-interval '1 minute'
		FROM generate_series(1,64) n`)
	if err != nil {
		t.Fatal(err)
	}
	healthyID := uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO outbox(outbox_id,aggregate_id,kind,payload,business_key)
		VALUES($1::uuid,'healthy','lease.changed','{}',$1::text)`, healthyID)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for range 3 {
		if err := processOutboxBatch(ctx, db, rdb, log); err != nil {
			t.Fatal(err)
		}
	}
	var handled bool
	if err := pool.QueryRow(ctx, `SELECT published_at IS NOT NULL FROM outbox WHERE outbox_id=$1`, healthyID).Scan(&handled); err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("healthy outbox row starved behind 64 poison rows after 3 batches")
	}
	messages, err := rdb.XRange(ctx, "relay:fleet:events", "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, msg := range messages {
		if msg.Values["event_id"] == healthyID {
			found = true
			cleanup, stop := context.WithTimeout(context.Background(), time.Second)
			err := rdb.XDel(cleanup, "relay:fleet:events", msg.ID).Err()
			stop()
			if err != nil {
				t.Error(err)
			}
		}
	}
	if !found {
		t.Fatal("completion marker has no matching real Redis event")
	}
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 64 {
		t.Fatalf("poison rows must remain recoverable: pending=%d", pending)
	}
	t.Log("64 malformed delivery envelopes retained; healthy event published within 3 polling batches")
}
