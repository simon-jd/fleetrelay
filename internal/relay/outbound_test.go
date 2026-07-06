package relay

import (
	"errors"
	"testing"
)

func TestBoundedReliableAndCoalescedTelemetry(t *testing.T) {
	q := NewOutbound(1)
	if err := q.PushReliable([]byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := q.PushReliable([]byte("two")); !errors.Is(err, ErrSlowConsumer) {
		t.Fatalf("want slow consumer: %v", err)
	}
	q.PutTelemetry("d", "battery", []byte("1"))
	q.PutTelemetry("d", "battery", []byte("2"))
	if q.TelemetrySize() != 1 {
		t.Fatal("telemetry cache grew")
	}
	if got := string(q.DrainTelemetry()["d\x00battery"]); got != "2" {
		t.Fatalf("got %q", got)
	}
}
