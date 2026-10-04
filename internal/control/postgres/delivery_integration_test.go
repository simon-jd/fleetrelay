package postgres_test

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fleetrelay/fleetrelay/internal/control"
	"github.com/fleetrelay/fleetrelay/internal/control/postgres"
	"github.com/fleetrelay/fleetrelay/internal/testutil"
)

func delivery(t *testing.T, db *postgres.Store, id string) control.OutboxItem {
	t.Helper()
	items, err := db.ClaimOutbox(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Kind == "command.delivery" && item.AggregateID == id {
			return item
		}
	}
	t.Fatal("no recoverable delivery intent")
	return control.OutboxItem{}
}

func TestAttemptAndDeliveryCommitTogether(t *testing.T) {
	db, pool, _ := testutil.Postgres(t)
	now := time.Now().UTC()
	c, sess := testutil.Command(t, db, now)
	// Fail at the delivery insert, after the attempt insert. A transaction must
	// roll both back and leave command.created recoverable.
	_, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_delivery() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN IF NEW.kind='command.delivery' THEN RAISE EXCEPTION 'injected delivery insert failure'; END IF; RETURN NEW; END $$;
	CREATE TRIGGER reject_delivery BEFORE INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION reject_delivery()`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateAttempt(t.Context(), c.ID, "gw-a", sess.Generation, now, now.Add(3*time.Second))
	if err == nil {
		t.Fatal("attempt committed without its delivery intent")
	}
	var attempts, intents, pending int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM command_attempts),
		(SELECT count(*) FROM outbox WHERE kind='command.delivery'),
		(SELECT count(*) FROM outbox WHERE kind='command.created' AND published_at IS NULL)`).Scan(&attempts, &intents, &pending); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || intents != 0 || pending != 1 {
		t.Fatalf("rollback: attempts=%d intents=%d pending=%d", attempts, intents, pending)
	}
	if _, err := pool.Exec(t.Context(), "DROP TRIGGER reject_delivery ON outbox"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateAttempt(t.Context(), c.ID, "gw-a", sess.Generation, now, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	item := delivery(t, db, c.ID)
	var d control.Delivery
	if err := json.Unmarshal(item.Payload, &d); err != nil {
		t.Fatal(err)
	}
	if d.Command.ID != c.ID || d.Attempt.Attempt != 1 || d.Attempt.TargetSessionGeneration != sess.Generation {
		t.Fatalf("wrong envelope: %+v", d)
	}
}

func TestUnpublishedAttemptSurvivesReopenWithoutBudgetConsumption(t *testing.T) {
	db, pool, dsn := testutil.Postgres(t)
	now := time.Now().UTC()
	c, sess := testutil.Command(t, db, now)
	if _, err := db.CreateAttempt(t.Context(), c.ID, "gw-a", sess.Generation, now, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	item := delivery(t, db, c.ID)
	db.Close()
	reopened, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := delivery(t, reopened, c.ID); got.ID != item.ID || string(got.Payload) != string(item.Payload) {
		t.Fatal("reopen changed delivery identity or envelope")
	}
	for i := 0; i < 5; i++ {
		later := now.Add(time.Duration(10+i) * time.Second)
		if _, err := reopened.CreateAttempt(t.Context(), c.ID, "gw-a", sess.Generation, later, later.Add(3*time.Second)); !errors.Is(err, control.ErrConflict) {
			t.Fatalf("unpublished attempt allowed another attempt: %v", err)
		}
		rows, err := reopened.ListRetryCandidates(t.Context(), later, 64)
		if err != nil || len(rows) != 0 {
			t.Fatalf("unpublished attempt entered retry scan: %v %v", rows, err)
		}
	}
	if _, handled, err := reopened.PrepareOutboxCommand(t.Context(), c.ID, now.Add(15*time.Second)); err != nil || !handled {
		t.Fatalf("replayed command.created was not consumed: %v %v", handled, err)
	}
	confirmed := now.Add(20 * time.Second)
	if err := reopened.MarkOutboxPublished(t.Context(), item.ID, confirmed); err != nil {
		t.Fatal(err)
	}
	if err := reopened.MarkOutboxPublished(t.Context(), item.ID, confirmed.Add(time.Second)); !errors.Is(err, control.ErrNotFound) {
		t.Fatalf("duplicate confirmation changed deadline: %v", err)
	}
	for _, check := range []struct {
		at    time.Time
		count int
	}{{confirmed.Add(2 * time.Second), 0}, {confirmed.Add(3 * time.Second), 1}} {
		rows, err := reopened.ListRetryCandidates(t.Context(), check.at, 64)
		if err != nil || len(rows) != check.count {
			t.Fatalf("ACK window: got %d want %d err=%v", len(rows), check.count, err)
		}
	}
	if _, err := reopened.CreateAttempt(t.Context(), c.ID, "gw-a", sess.Generation, confirmed.Add(3*time.Second), confirmed.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	// This test creates attempt 2 at a synthetic future instant. Inspect the
	// persisted intent directly; the due-work poll uses PostgreSQL's real clock.
	var nextID string
	if err := pool.QueryRow(t.Context(), `SELECT outbox_id FROM outbox WHERE aggregate_id=$1 AND kind='command.delivery' AND published_at IS NULL`, c.ID).Scan(&nextID); err != nil {
		t.Fatal(err)
	}
	if nextID == item.ID {
		t.Fatal("new device attempt reused old delivery ID")
	}
}

func TestConcurrentAttemptCreatorsHaveOneDurableIntent(t *testing.T) {
	db, pool, _ := testutil.Postgres(t)
	now := time.Now().UTC()
	c, sess := testutil.Command(t, db, now)
	var wg sync.WaitGroup
	var wins atomic.Int32
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			<-start
			if _, err := db.CreateAttempt(t.Context(), c.ID, "gw-a", sess.Generation, now, now.Add(3*time.Second)); err == nil {
				wins.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()
	var attempts, intents, created int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM command_attempts),
		(SELECT count(*) FROM outbox WHERE kind='command.delivery'),
		(SELECT count(*) FROM outbox WHERE kind='command.created' AND published_at IS NULL)`).Scan(&attempts, &intents, &created); err != nil {
		t.Fatal(err)
	}
	if wins.Load() != 1 || attempts != 1 || intents != 1 || created != 0 {
		t.Fatalf("wins=%d attempts=%d intents=%d pending_created=%d", wins.Load(), attempts, intents, created)
	}
}

func TestDeliveryFencesAndExpiry(t *testing.T) {
	for _, scenario := range []string{"session_replacement", "lease_release", "command_expiry", "terminal_ack"} {
		t.Run(scenario, func(t *testing.T) {
			db, _, _ := testutil.Postgres(t)
			now := time.Now().UTC()
			c, sess := testutil.Command(t, db, now)
			a, err := db.CreateAttempt(t.Context(), c.ID, "gw-a", sess.Generation, now, now.Add(3*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			d := control.Delivery{Command: c, Attempt: a}
			if valid, err := db.DeliveryCurrent(t.Context(), d, now); err != nil || !valid {
				t.Fatalf("valid delivery: %v %v", valid, err)
			}
			switch scenario {
			case "session_replacement":
				_, err = db.RegisterSession(t.Context(), control.Device{ID: c.DeviceID, AdapterType: "sim"}, "gw-b", now)
			case "lease_release":
				_, err = db.ReleaseLease(t.Context(), c.LeaseID, "alice", now)
			case "command_expiry":
				now = c.ExpiresAt.Add(time.Second)
				rows, listErr := db.ListRetryCandidates(t.Context(), now, 64)
				if listErr != nil || len(rows) != 1 {
					t.Fatalf("unpublished expiry was hidden: %v %v", rows, listErr)
				}
				var changed bool
				var expired control.Command
				expired, changed, err = db.ResolveAttemptDeadline(t.Context(), c.ID, 3, now)
				if err == nil && (!changed || expired.Status != control.StatusExpired) {
					t.Fatal("command did not expire")
				}
			case "terminal_ack":
				_, _, err = db.Acknowledge(t.Context(), control.Ack{CommandID: c.ID, DeviceID: c.DeviceID, GatewayID: "gw-a", SessionGeneration: sess.Generation, LeaseEpoch: c.LeaseEpoch, DeviceSequence: c.DeviceSequence, Result: control.StatusSucceeded}, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			if valid, err := db.DeliveryCurrent(t.Context(), d, now); err != nil || valid {
				t.Fatalf("obsolete delivery accepted: %v %v", valid, err)
			}
		})
	}
}
