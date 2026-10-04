package main

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fleetrelay/fleetrelay/internal/testutil"
	"github.com/redis/go-redis/v9"
)

func TestOutboxMetricsIncludeDelayedRowsAndFailClosed(t *testing.T) {
	db, pool, _ := testutil.Postgres(t)
	ctx := t.Context()
	_, err := pool.Exec(ctx, `INSERT INTO outbox(aggregate_id,kind,payload,business_key,created_at,next_publish_at)
		VALUES('ready','lease.changed','{}','ready',now()-interval '2 minutes',now()),
		('waiting','lease.changed','{}','waiting',now()-interval '1 minute',now()+interval '1 hour')`)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := db.OutboxStats(ctx)
	if err != nil || stats.Pending != 2 || stats.RetryWaiting != 1 || stats.OldestSeconds < 120 {
		t.Fatalf("stats %+v %v", stats, err)
	}
	// Exercise the actual HTTP router; this scrape does not need Redis.
	handler := dispatcherHTTP(db, nil)
	scrape := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
		return w
	}
	w := scrape()
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("scrape: %d %v", w.Code, w.Header())
	}
	for _, sample := range []string{"fleetrelay_outbox_pending 2\n", "fleetrelay_outbox_retry_waiting 1\n", "outbox_oldest_unpublished_seconds 120."} {
		// The precise age is checked against the database above; only require
		// the age series in exposition, independent of workstation speed.
		if strings.HasPrefix(sample, "outbox_oldest") {
			sample = "\noutbox_oldest_unpublished_seconds "
		}
		if !strings.Contains(w.Body.String(), sample) {
			t.Fatalf("missing %s: %s", sample, w.Body.String())
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox SET published_at=now()`); err != nil {
		t.Fatal(err)
	}
	w = scrape()
	if w.Code != 200 || !strings.Contains(w.Body.String(), "fleetrelay_outbox_pending 0\n") || !strings.Contains(w.Body.String(), "outbox_oldest_unpublished_seconds 0.000000\n") {
		t.Fatal("drained backlog not zero")
	}
	db.Close()
	w = scrape()
	if w.Code != 503 || strings.Contains(w.Body.String(), "fleetrelay_outbox_pending") {
		t.Fatalf("database failure looked healthy: %d %s", w.Code, w.Body.String())
	}
}

type slowFirstXADD struct {
	xaddFailure
	calls int
}

func (h *slowFirstXADD) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "xadd" {
			h.calls++
			if h.calls == 1 {
				<-ctx.Done()
				return ctx.Err()
			}
		}
		return next(ctx, cmd)
	}
}

func TestOutboxItemTimeoutDoesNotCancelNextRow(t *testing.T) {
	db, pool, _ := testutil.Postgres(t)
	ctx, stop := context.WithTimeout(t.Context(), 8*time.Second)
	defer stop()
	rdb := redis.NewClient(dispatcherRedisOptions(testutil.RequiredEnv(t, "FLEETRELAY_TEST_REDIS_ADDR")))
	defer rdb.Close()
	hook := &slowFirstXADD{}
	rdb.AddHook(hook)
	_, err := pool.Exec(ctx, `INSERT INTO outbox(aggregate_id,kind,payload,business_key,created_at,next_publish_at)
		VALUES('slow','lease.changed','{}','slow',now()-interval '1 minute',now()-interval '1 minute'),
		('healthy','lease.changed','{}','healthy',now(),now())`)
	if err != nil {
		t.Fatal(err)
	}
	if err := processOutboxBatch(ctx, db, rdb, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	var deferred, handled bool
	if err := pool.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM outbox WHERE business_key='slow' AND published_at IS NULL AND publication_failures=1),
		EXISTS(SELECT 1 FROM outbox WHERE business_key='healthy' AND published_at IS NOT NULL AND publication_failures=0)`).Scan(&deferred, &handled); err != nil {
		t.Fatal(err)
	}
	if !deferred || !handled || hook.calls != 2 {
		t.Fatalf("per-item isolation: deferred=%v handled=%v calls=%d", deferred, handled, hook.calls)
	}
}
