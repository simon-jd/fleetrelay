package control_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fleetrelay/fleetrelay/internal/control"
	"github.com/fleetrelay/fleetrelay/internal/control/memory"
)

func fixture(t *testing.T) (*control.Service, *memory.Store, control.DeviceSession, time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 31, 20, 0, 0, 0, time.UTC)
	store := memory.New()
	svc := control.NewService(store, nil, 30*time.Second).WithClock(func() time.Time { return now })
	sess, err := svc.RegisterSession(context.Background(), control.Device{ID: "vacuum-2", AdapterType: "sim", Capabilities: []string{"PAUSE_TASK", "RETURN_HOME"}}, "gw-a")
	if err != nil {
		t.Fatal(err)
	}
	return svc, store, sess, now
}
func TestLeaseFencingAndIdempotentCommand(t *testing.T) {
	svc, store, sess, now := fixture(t)
	ctx := context.Background()
	lease, err := svc.AcquireLease(ctx, control.RoleOperator, "vacuum-2", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.AcquireLease(ctx, control.RoleOperator, "vacuum-2", "bob"); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	req := control.CreateCommandRequest{OperatorID: "alice", Role: control.RoleOperator, DeviceID: "vacuum-2", LeaseID: lease.ID, IdempotencyKey: "browser-1", Type: "PAUSE_TASK", Payload: json.RawMessage(`{}`), DeliveryPolicy: control.RequireOnline, ExpiresAt: now.Add(time.Minute)}
	c, duplicate, err := svc.CreateCommand(ctx, req)
	if err != nil || duplicate {
		t.Fatalf("create: duplicate=%v err=%v", duplicate, err)
	}
	again, duplicate, err := svc.CreateCommand(ctx, req)
	if err != nil || !duplicate || again.ID != c.ID {
		t.Fatalf("idempotency failed: %#v %v %v", again, duplicate, err)
	}
	if _, err = store.CreateAttempt(ctx, c.ID, "gw-a", sess.Generation, now, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	receipt := control.DispatchReceipt{CommandID: c.ID, DeviceID: c.DeviceID, GatewayID: "gw-a", SessionGeneration: sess.Generation}
	if _, _, err = svc.MarkDispatched(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	ack := control.Ack{CommandID: c.ID, DeviceID: c.DeviceID, GatewayID: "gw-a", SessionGeneration: sess.Generation, LeaseEpoch: c.LeaseEpoch, DeviceSequence: c.DeviceSequence, Result: control.StatusSucceeded, Detail: "ok"}
	done, dup, err := svc.Acknowledge(ctx, ack)
	if err != nil || dup || done.Status != control.StatusSucceeded {
		t.Fatalf("ack: %#v %v %v", done, dup, err)
	}
	_, dup, err = svc.Acknowledge(ctx, ack)
	if err != nil || !dup {
		t.Fatalf("duplicate ack changed state: %v %v", dup, err)
	}
}
func TestStaleSessionAndLateAckReconciliation(t *testing.T) {
	svc, store, old, now := fixture(t)
	ctx := context.Background()
	lease, _ := svc.AcquireLease(ctx, control.RoleOperator, "vacuum-2", "alice")
	c, _, _ := svc.CreateCommand(ctx, control.CreateCommandRequest{OperatorID: "alice", Role: control.RoleOperator, DeviceID: "vacuum-2", LeaseID: lease.ID, IdempotencyKey: "x", Type: "RETURN_HOME", Payload: json.RawMessage(`{}`), DeliveryPolicy: control.RequireOnline, ExpiresAt: now.Add(time.Minute)})
	store.CreateAttempt(ctx, c.ID, "gw-a", old.Generation, now, now.Add(time.Second))
	svc.MarkDispatched(ctx, control.DispatchReceipt{CommandID: c.ID, DeviceID: c.DeviceID, GatewayID: "gw-a", SessionGeneration: old.Generation})
	svc.RegisterSession(ctx, control.Device{ID: "vacuum-2", AdapterType: "sim", Capabilities: []string{"RETURN_HOME"}}, "gw-b")
	ack := control.Ack{CommandID: c.ID, DeviceID: c.DeviceID, GatewayID: "gw-a", SessionGeneration: old.Generation, LeaseEpoch: c.LeaseEpoch, DeviceSequence: c.DeviceSequence, Result: control.StatusSucceeded, Detail: "late"}
	if _, _, err := svc.Acknowledge(ctx, ack); !errors.Is(err, control.ErrStaleSession) {
		t.Fatalf("want stale session: %v", err)
	}
}
func TestConcurrentLeaseHasSingleWinner(t *testing.T) {
	svc, _, _, _ := fixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var wins int
	var mu sync.Mutex
	for _, op := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.AcquireLease(ctx, control.RoleOperator, "vacuum-2", op); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("wins=%d", wins)
	}
}
func TestStateMachineProperty(t *testing.T) {
	statuses := []control.CommandStatus{control.StatusAccepted, control.StatusPendingTransport, control.StatusDispatched, control.StatusSucceeded, control.StatusFailed, control.StatusAckTimeout, control.StatusExpired, control.StatusCancelled, control.StatusRejected}
	for _, from := range statuses {
		for _, to := range statuses {
			got := control.CanTransition(from, to)
			if (from == control.StatusSucceeded || from == control.StatusFailed || from == control.StatusExpired || from == control.StatusCancelled || from == control.StatusRejected) && got {
				t.Fatalf("terminal %s transitions to %s", from, to)
			}
		}
	}
}

func TestFastAckIsAtomicAndBoundToAttempt(t *testing.T) {
	svc, store, sess, now := fixture(t)
	ctx := context.Background()
	lease, _ := svc.AcquireLease(ctx, control.RoleOperator, "vacuum-2", "alice")
	c, _, _ := svc.CreateCommand(ctx, control.CreateCommandRequest{OperatorID: "alice", Role: control.RoleOperator, DeviceID: "vacuum-2", LeaseID: lease.ID, IdempotencyKey: "fast", Type: "PAUSE_TASK", Payload: json.RawMessage(`{}`), DeliveryPolicy: control.RequireOnline, ExpiresAt: now.Add(time.Minute)})
	if _, err := store.CreateAttempt(ctx, c.ID, "gw-a", sess.Generation, now, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	bad := control.Ack{CommandID: c.ID, DeviceID: c.DeviceID, GatewayID: "gw-a", SessionGeneration: sess.Generation, LeaseEpoch: c.LeaseEpoch, DeviceSequence: c.DeviceSequence + 1, Result: control.StatusSucceeded}
	if _, _, err := svc.Acknowledge(ctx, bad); !errors.Is(err, control.ErrInvalid) {
		t.Fatalf("wrong sequence accepted: %v", err)
	}
	bad.DeviceSequence = c.DeviceSequence
	done, dup, err := svc.Acknowledge(ctx, bad)
	if err != nil || dup || done.Status != control.StatusSucceeded {
		t.Fatalf("fast ACK: %#v dup=%v err=%v", done, dup, err)
	}
	if _, dup, err = svc.MarkDispatched(ctx, control.DispatchReceipt{CommandID: c.ID, DeviceID: c.DeviceID, GatewayID: "gw-a", SessionGeneration: sess.Generation}); err != nil || !dup {
		t.Fatalf("late dispatch reconciliation: dup=%v err=%v", dup, err)
	}
}

func TestListDevicesReflectsPresence(t *testing.T) {
	svc, _, _, _ := fixture(t)
	devices, err := svc.ListDevices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].ID != "vacuum-2" || devices[0].State != "ONLINE" {
		t.Fatalf("unexpected devices: %#v", devices)
	}
}
