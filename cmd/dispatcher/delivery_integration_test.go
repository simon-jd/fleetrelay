package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/fleetrelay/fleetrelay/internal/control"
	"github.com/fleetrelay/fleetrelay/internal/control/postgres"
	"github.com/fleetrelay/fleetrelay/internal/testutil"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// A real XADD with an injected failure at either side of its network boundary.
// This models an ambiguous response without requiring a timing-sensitive kill.
type xaddFailure struct{ after bool }

func (h xaddFailure) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) { return next(ctx, network, addr) }
}
func (h xaddFailure) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h xaddFailure) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() != "xadd" {
			return next(ctx, cmd)
		}
		if h.after {
			if err := next(ctx, cmd); err != nil {
				return err
			}
		}
		return errors.New("injected XADD response failure")
	}
}

func pending(t *testing.T, db *postgres.Store, commandID, kind string) control.OutboxItem {
	t.Helper()
	rows, err := db.ClaimOutbox(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range rows {
		if item.AggregateID == commandID && item.Kind == kind {
			return item
		}
	}
	t.Fatalf("missing %s for %s", kind, commandID)
	return control.OutboxItem{}
}

func TestRedisPublicationFailureReusesDurableDelivery(t *testing.T) {
	for _, after := range []bool{false, true} {
		name := "before_xadd"
		if after {
			name = "after_xadd_lost_reply"
		}
		t.Run(name, func(t *testing.T) {
			addr := testutil.RequiredEnv(t, "FLEETRELAY_TEST_REDIS_ADDR")
			db, pool, dsn := testutil.Postgres(t)
			ctx := t.Context()
			now := time.Now().UTC()
			c, _ := testutil.Command(t, db, now)
			gateway := "gw-" + uuid.NewString()
			sess, err := db.RegisterSession(ctx, control.Device{ID: c.DeviceID, AdapterType: "sim"}, gateway, now)
			if err != nil {
				t.Fatal(err)
			}
			opts := &redis.Options{Addr: addr, MaxRetries: -1}
			healthy := redis.NewClient(opts)
			defer healthy.Close()
			if err := healthy.Ping(ctx).Err(); err != nil {
				t.Fatal(err)
			}
			stream := "relay:gateway:" + gateway + ":commands"
			presenceKey := "relay:presence:" + c.DeviceID
			aliveKey := "relay:gateway:" + gateway + ":alive"
			t.Cleanup(func() {
				client := redis.NewClient(opts)
				defer client.Close()
				ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				if err := client.Del(ctx, stream, presenceKey, aliveKey).Err(); err != nil {
					t.Error(err)
				}
			})
			p, _ := json.Marshal(presence{GatewayID: gateway, GatewayInstance: "instance-1", Generation: sess.Generation})
			if err := healthy.Set(ctx, presenceKey, p, time.Minute).Err(); err != nil {
				t.Fatal(err)
			}
			if err := healthy.Set(ctx, aliveKey, "instance-1", time.Minute).Err(); err != nil {
				t.Fatal(err)
			}
			created := pending(t, db, c.ID, "command.created")
			if err := publish(ctx, db, healthy, created); err != nil {
				t.Fatal(err)
			}
			intent := pending(t, db, c.ID, "command.delivery")
			broken := redis.NewClient(opts)
			broken.AddHook(xaddFailure{after: after})
			if err := publish(ctx, db, broken, intent); err == nil {
				t.Fatal("injected publish succeeded")
			}
			broken.Close()
			if !after {
				for i := 0; i < 4; i++ {
					failed := redis.NewClient(opts)
					failed.AddHook(xaddFailure{})
					err := publish(ctx, db, failed, intent)
					failed.Close()
					if err == nil {
						t.Fatal("publish failure not injected")
					}
				}
			}
			before, err := healthy.XLen(ctx, stream).Result()
			wantBefore := int64(0)
			if after {
				wantBefore = 1
			}
			if err != nil || before != wantBefore {
				t.Fatalf("XADD boundary: len=%d err=%v", before, err)
			}
			// Even an expired device deadline cannot consume another attempt
			// while the original publication result remains unknown.
			if _, err := pool.Exec(ctx, `UPDATE command_attempts SET attempt_deadline=$2 WHERE command_id=$1`, c.ID, now.Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			retryDeadlines(ctx, db, healthy, slog.New(slog.NewTextHandler(io.Discard, nil)), 3)
			// Close/reopen adapters to prove recovery uses committed state.
			db.Close()
			recovered, err := postgres.Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			replay := pending(t, recovered, c.ID, "command.delivery")
			if replay.ID != intent.ID || string(replay.Payload) != string(intent.Payload) {
				t.Fatal("recovery changed intent")
			}
			// A stale command.created batch must not generate another attempt.
			if err := publish(ctx, recovered, healthy, created); err != nil {
				t.Fatal(err)
			}
			if err := publish(ctx, recovered, healthy, replay); err != nil {
				t.Fatal(err)
			}
			if err := recovered.MarkOutboxPublished(ctx, replay.ID, time.Now()); err != nil {
				t.Fatal(err)
			}
			messages, err := healthy.XRange(ctx, stream, "-", "+").Result()
			if err != nil || int64(len(messages)) != wantBefore+1 {
				t.Fatalf("replay messages=%d err=%v", len(messages), err)
			}
			for _, m := range messages {
				if m.Values["delivery_id"] != intent.ID || m.Values["command_id"] != c.ID || m.Values["attempt"] != "1" {
					t.Fatalf("changed delivery identity: %v", m.Values)
				}
			}
			var attempts, intents int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM command_attempts WHERE command_id=$1::uuid), (SELECT count(*) FROM outbox WHERE aggregate_id=$1::text AND kind='command.delivery')`, c.ID).Scan(&attempts, &intents); err != nil {
				t.Fatal(err)
			}
			if attempts != 1 || intents != 1 {
				t.Fatalf("budget consumed by publication retries: %d attempts %d intents", attempts, intents)
			}
		})
	}
}

func TestConfirmationFailureDefersSameDelivery(t *testing.T) {
	db, pool, _ := testutil.Postgres(t)
	ctx := t.Context()
	now := time.Now().UTC()
	c, _ := testutil.Command(t, db, now)
	gateway := "confirm-" + uuid.NewString()
	sess, err := db.RegisterSession(ctx, control.Device{ID: c.DeviceID, AdapterType: "sim"}, gateway, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateAttempt(ctx, c.ID, gateway, sess.Generation, now, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	intent := pending(t, db, c.ID, "command.delivery")
	rdb := redis.NewClient(dispatcherRedisOptions(testutil.RequiredEnv(t, "FLEETRELAY_TEST_REDIS_ADDR")))
	defer rdb.Close()
	stream := "relay:gateway:" + gateway + ":commands"
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		client := redis.NewClient(dispatcherRedisOptions(testutil.RequiredEnv(t, "FLEETRELAY_TEST_REDIS_ADDR")))
		defer client.Close()
		if err := client.Del(cleanup, stream).Err(); err != nil {
			t.Error(err)
		}
	})
	_, err = pool.Exec(ctx, `CREATE FUNCTION reject_confirmation() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN IF NEW.kind='command.delivery' AND NEW.published_at IS NOT NULL THEN RAISE EXCEPTION 'injected confirmation failure'; END IF; RETURN NEW; END $$;
	CREATE TRIGGER reject_confirmation BEFORE UPDATE ON outbox FOR EACH ROW EXECUTE FUNCTION reject_confirmation()`)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := processOutboxBatch(ctx, db, rdb, log); err != nil {
		t.Fatal(err)
	}
	var deferred bool
	if err := pool.QueryRow(ctx, `SELECT publication_failures=1 AND published_at IS NULL AND next_publish_at>now() FROM outbox WHERE outbox_id=$1`, intent.ID).Scan(&deferred); err != nil {
		t.Fatal(err)
	}
	if n, err := rdb.XLen(ctx, stream).Result(); err != nil || n != 1 || !deferred {
		t.Fatalf("ambiguous confirmation not deferred: messages=%d deferred=%v error=%v", n, deferred, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_confirmation ON outbox`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox SET next_publish_at=now() WHERE outbox_id=$1`, intent.ID); err != nil {
		t.Fatal(err)
	}
	if err := processOutboxBatch(ctx, db, rdb, log); err != nil {
		t.Fatal(err)
	}
	messages, err := rdb.XRange(ctx, stream, "-", "+").Result()
	if err != nil || len(messages) != 2 {
		t.Fatalf("retry messages=%d error=%v", len(messages), err)
	}
	for _, msg := range messages {
		if msg.Values["delivery_id"] != intent.ID || msg.Values["attempt"] != "1" {
			t.Fatal("confirmation failure changed delivery identity")
		}
	}
	var attempts int
	var handled bool
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM command_attempts),published_at IS NOT NULL FROM outbox WHERE outbox_id=$1`, intent.ID).Scan(&attempts, &handled); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || !handled {
		t.Fatalf("attempts=%d handled=%v", attempts, handled)
	}
}
