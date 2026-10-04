package postgres_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/fleetrelay/fleetrelay/internal/control"
	"github.com/fleetrelay/fleetrelay/internal/control/postgres"
	"github.com/fleetrelay/fleetrelay/internal/testutil"
)

func TestOutboxRetrySurvivesReopenAndRejectsStaleFailure(t *testing.T) {
	db, pool, dsn := testutil.Postgres(t)
	ctx := t.Context()
	now := time.Now().UTC()
	c, sess := testutil.Command(t, db, now)
	if _, err := db.CreateAttempt(ctx, c.ID, "gw-a", sess.Generation, now, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	item := delivery(t, db, c.ID)
	if changed, err := db.DeferOutbox(ctx, item, 30*time.Second); err != nil || !changed {
		t.Fatalf("defer: %v %v", changed, err)
	}
	var due time.Time
	var failures int
	if err := pool.QueryRow(ctx, `SELECT next_publish_at,publication_failures FROM outbox WHERE outbox_id=$1`, item.ID).Scan(&due, &failures); err != nil {
		t.Fatal(err)
	}
	if failures != 1 || !due.After(now.Add(29*time.Second)) {
		t.Fatalf("retry not persisted: %v %d", due, failures)
	}
	db.Close()
	reopened, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if changed, err := reopened.DeferOutbox(ctx, item, time.Second); err != nil || changed {
		t.Fatalf("stale failure rescheduled: %v %v", changed, err)
	}
	var unchanged time.Time
	if err := pool.QueryRow(ctx, `SELECT next_publish_at FROM outbox WHERE outbox_id=$1`, item.ID).Scan(&unchanged); err != nil {
		t.Fatal(err)
	}
	if !unchanged.Equal(due) {
		t.Fatal("stale batch changed persisted retry deadline")
	}
	rows, err := reopened.ClaimOutbox(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == item.ID {
			t.Fatal("retry polled before due")
		}
	}
	// Transport backoff must preserve the device retry budget even when its
	// original (unconfirmed) ACK deadline has passed.
	if _, err := reopened.CreateAttempt(ctx, c.ID, "gw-a", sess.Generation, now.Add(5*time.Second), now.Add(8*time.Second)); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("extra device attempt: %v", err)
	}
	// Make this one row eligible without sleeping or changing command time.
	if _, err := pool.Exec(ctx, `UPDATE outbox SET next_publish_at=now()-interval '1 second' WHERE outbox_id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	retry := delivery(t, reopened, c.ID)
	if retry.ID != item.ID || string(retry.Payload) != string(item.Payload) || retry.PublicationFailures != 1 {
		t.Fatal("retry identity, payload or failure count changed")
	}
	if err := reopened.MarkOutboxPublished(ctx, item.ID, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if changed, err := reopened.DeferOutbox(ctx, retry, time.Second); err != nil || changed {
		t.Fatalf("handled row revived: %v %v", changed, err)
	}
	var attempts int
	var deadline time.Time
	if err := pool.QueryRow(ctx, `SELECT count(*),max(attempt_deadline) FROM command_attempts WHERE command_id=$1`, c.ID).Scan(&attempts, &deadline); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || !deadline.Equal(now.Add(13*time.Second).Truncate(time.Microsecond)) {
		t.Fatalf("ACK deadline/budget: %d %v", attempts, deadline)
	}
}

func TestOutboxRetryMigrationPreservesExistingRows(t *testing.T) {
	_, pool, _ := testutil.Postgres(t)
	ctx := t.Context()
	_, file, _, _ := runtime.Caller(0)
	apply := func(name string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../../migrations/"+name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	apply("000002_outbox_retry.down.sql")
	_, err := pool.Exec(ctx, `INSERT INTO outbox(aggregate_id,kind,payload,business_key,created_at,published_at)
		VALUES('old-pending','lease.changed','{}','old-pending','2026-01-01',NULL),
		('old-handled','lease.changed','{}','old-handled','2026-01-02','2026-01-03')`)
	if err != nil {
		t.Fatal(err)
	}
	apply("000002_outbox_retry.up.sql")
	var count, ready, handled int
	if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE next_publish_at=created_at AND publication_failures=0),count(*) FILTER (WHERE published_at IS NOT NULL) FROM outbox`).Scan(&count, &ready, &handled); err != nil {
		t.Fatal(err)
	}
	if count != 2 || ready != 2 || handled != 1 {
		t.Fatalf("migration lost state: %d %d %d", count, ready, handled)
	}
	apply("000002_outbox_retry.down.sql")
	if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE published_at IS NOT NULL) FROM outbox`).Scan(&count, &handled); err != nil {
		t.Fatal(err)
	}
	if count != 2 || handled != 1 {
		t.Fatal("down migration lost outbox rows/markers")
	}
	apply("000002_outbox_retry.up.sql")
}

func TestOutboxDeferralCancellationAndBounds(t *testing.T) {
	db, pool, _ := testutil.Postgres(t)
	ctx := t.Context()
	_, err := pool.Exec(ctx, `INSERT INTO outbox(aggregate_id,kind,payload,business_key) VALUES('test','lease.changed','{}','test')`)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.ClaimOutbox(ctx, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("fixture %v %v", rows, err)
	}
	for _, delay := range []time.Duration{0, -time.Second, 31 * time.Second} {
		if _, err := db.DeferOutbox(ctx, rows[0], delay); !errors.Is(err, control.ErrInvalid) {
			t.Fatalf("unbounded delay accepted: %v", err)
		}
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if changed, err := db.DeferOutbox(cancelled, rows[0], time.Second); !errors.Is(err, context.Canceled) || changed {
		t.Fatalf("cancelled deferral mutated state: %v %v", changed, err)
	}
	stats, err := db.OutboxStats(ctx)
	if err != nil || stats.Pending != 1 || stats.RetryWaiting != 0 {
		t.Fatalf("invalid delay mutated state: %+v %v", stats, err)
	}
}
