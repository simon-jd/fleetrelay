// Package testutil provisions isolated schemas in an explicitly supplied test
// database. It never uses the application's DATABASE_URL.
package testutil

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fleetrelay/fleetrelay/internal/control"
	"github.com/fleetrelay/fleetrelay/internal/control/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func RequiredEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		if os.Getenv("FLEETRELAY_REQUIRE_INTEGRATION") == "1" {
			t.Fatalf("%s is required", key)
		}
		t.Skipf("integration test requires %s", key)
	}
	return v
}

func Postgres(t *testing.T) (*postgres.Store, *pgxpool.Pool, string) {
	t.Helper()
	dsn := RequiredEnv(t, "FLEETRELAY_TEST_DATABASE_URL")
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + uuid.New().String()[:8]
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	isolated := u.String()
	pool, err := pgxpool.New(ctx, isolated)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, file, _, _ := runtime.Caller(0)
	files, err := filepath.Glob(filepath.Join(filepath.Dir(file), "../../migrations/*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found")
	}
	for _, path := range files {
		sql, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// PostgreSQL 17 provides gen_random_uuid in core. Avoid installing a
		// database-global optional extension into a disposable test schema.
		ddl := strings.Replace(string(sql), "CREATE EXTENSION IF NOT EXISTS pgcrypto;", "", 1)
		if _, err = pool.Exec(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	store, err := postgres.Open(ctx, isolated)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store, pool, isolated
}

func Command(t *testing.T, db *postgres.Store, now time.Time) (control.Command, control.DeviceSession) {
	t.Helper()
	ctx := t.Context()
	sess, err := db.RegisterSession(ctx, control.Device{ID: uuid.NewString(), AdapterType: "sim", Capabilities: []string{"PAUSE_TASK"}}, "gw-a", now)
	if err != nil {
		t.Fatal(err)
	}
	l, err := db.AcquireLease(ctx, sess.DeviceID, "alice", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := db.CreateCommand(ctx, control.CreateCommandRequest{
		OperatorID: "alice", Role: control.RoleOperator, DeviceID: sess.DeviceID,
		LeaseID: l.ID, IdempotencyKey: uuid.NewString(), Type: "PAUSE_TASK",
		Payload: json.RawMessage(`{}`), DeliveryPolicy: control.RequireOnline, ExpiresAt: now.Add(time.Minute),
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return c, sess
}
