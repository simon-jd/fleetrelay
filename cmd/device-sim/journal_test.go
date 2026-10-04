package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fleetrelayv1 "github.com/fleetrelay/fleetrelay/gen/fleetrelay/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func testCommand() *fleetrelayv1.Command {
	p, _ := structpb.NewStruct(map[string]any{"task": "synthetic"})
	return &fleetrelayv1.Command{CommandId: "cmd-1", IdempotencyKey: "idem-1", CommandType: "PAUSE_TASK", Payload: p, DeviceSequence: 1, LeaseEpoch: 1, SessionGeneration: 1, ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))}
}

func TestJournalReplayAfterReopenAndFingerprintConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	s, err := openDeviceState(path, "sim-1")
	if err != nil {
		t.Fatal(err)
	}
	c := testCommand()
	ack, fresh, err := s.execute(t.Context(), "sim-1", 1, c, 0)
	if err != nil || !fresh || ack.Result != fleetrelayv1.AckResult_ACK_RESULT_SUCCEEDED {
		t.Fatalf("first execution: %v %v %v", ack, fresh, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openDeviceState(path, "sim-1")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c.SessionGeneration = 2
	ack, fresh, err = s.execute(t.Context(), "sim-1", 2, c, 0)
	if err != nil || fresh || ack.Result != fleetrelayv1.AckResult_ACK_RESULT_SUCCEEDED || s.snapshot.Effects != 1 {
		t.Fatal("restart replay did not retain the original effect/result")
	}
	c.CommandType = "RETURN_HOME"
	ack, fresh, err = s.execute(t.Context(), "sim-1", 2, c, 0)
	if err != nil || fresh || ack.Detail != "command_fingerprint_conflict" || s.snapshot.Effects != 1 {
		t.Fatal("same ID with changed content was accepted")
	}
}

func TestJournalRejectsCorruptionWrongDeviceAndConcurrentOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	s, err := openDeviceState(path, "sim-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openDeviceState(path, "sim-1"); err == nil {
		t.Fatal("second owner accepted")
	}
	if _, _, err := s.execute(t.Context(), "sim-1", 1, testCommand(), 0); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if other, err := openDeviceState(path, "sim-2"); err == nil {
		other.Close()
		t.Fatal("wrong device accepted existing journal")
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"executed":`), 0600); err != nil {
		t.Fatal(err)
	}
	if other, err := openDeviceState(path, "sim-1"); err == nil {
		other.Close()
		t.Fatal("corrupt journal silently reset")
	}
}

func TestJournalPersistenceFailureRequiresRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := openDeviceState(filepath.Join(dir, "journal.json"), "sim-1")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Point the write path at a missing directory, leaving the owned lock open.
	s.path = filepath.Join(dir, "missing", "journal.json")
	if _, _, err := s.execute(t.Context(), "sim-1", 1, testCommand(), 0); err == nil {
		t.Fatal("failed disk write acknowledged")
	}
	if !s.failed || s.snapshot.Effects != 0 {
		t.Fatal("failed write mutated execution state")
	}
	s.path = filepath.Join(dir, "journal.json")
	if _, _, err := s.execute(t.Context(), "sim-1", 1, testCommand(), 0); err == nil {
		t.Fatal("continued after ambiguous persistence failure")
	}
}

func TestJournalConcurrentDuplicateHasOneSimulatedEffect(t *testing.T) {
	s, err := openDeviceState(filepath.Join(t.TempDir(), "journal.json"), "sim-1")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := testCommand()
	var wg sync.WaitGroup
	var freshCount atomic.Int32
	for range 16 {
		wg.Go(func() {
			ack, fresh, err := s.execute(t.Context(), "sim-1", 1, c, 0)
			if err != nil || ack.Result != fleetrelayv1.AckResult_ACK_RESULT_SUCCEEDED {
				t.Error("duplicate failed", err)
				return
			}
			if fresh {
				freshCount.Add(1)
			}
		})
	}
	wg.Wait()
	if freshCount.Load() != 1 || s.snapshot.Effects != 1 {
		t.Fatal("concurrent duplicate executed twice")
	}
}

func TestJournalCancellationAndFences(t *testing.T) {
	s := newDeviceState()
	c := testCommand()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := s.execute(ctx, "sim-1", 1, c, time.Second); err == nil {
		t.Fatal("cancelled execution succeeded")
	}
	if s.snapshot.Effects != 0 {
		t.Fatal("cancelled command changed effect count")
	}
	if _, _, err := s.execute(t.Context(), "sim-1", 1, c, 0); err != nil {
		t.Fatal(err)
	}
	stale := proto.Clone(c).(*fleetrelayv1.Command)
	stale.CommandId = "cmd-2"
	ack, _, err := s.execute(t.Context(), "sim-1", 1, stale, 0)
	if err != nil || ack.Detail != "stale_sequence" {
		t.Fatal("sequence fence bypassed")
	}
	c.SessionGeneration = 0
	ack, _, err = s.execute(t.Context(), "sim-1", 1, c, 0)
	if err != nil || ack.Detail != "stale_session" {
		t.Fatal("cached result bypassed session fence")
	}
}
