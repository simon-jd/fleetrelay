package relay

import (
	"errors"
	"sync"
)

var ErrSlowConsumer = errors.New("reliable outbound queue full")

// Outbound isolates reliable control events from coalescible telemetry. It is
// deliberately transport-agnostic so the exact backpressure rules are tested.
type Outbound struct {
	reliable  chan []byte
	mu        sync.Mutex
	telemetry map[string][]byte
}

func NewOutbound(capacity int) *Outbound {
	if capacity < 1 {
		capacity = 1
	}
	return &Outbound{reliable: make(chan []byte, capacity), telemetry: make(map[string][]byte)}
}
func (q *Outbound) PushReliable(v []byte) error {
	b := append([]byte(nil), v...)
	select {
	case q.reliable <- b:
		return nil
	default:
		return ErrSlowConsumer
	}
}
func (q *Outbound) Reliable() <-chan []byte { return q.reliable }
func (q *Outbound) PutTelemetry(device, topic string, v []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.telemetry[device+"\x00"+topic] = append([]byte(nil), v...)
}
func (q *Outbound) DrainTelemetry() map[string][]byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.telemetry
	q.telemetry = make(map[string][]byte)
	return out
}
func (q *Outbound) TelemetrySize() int { q.mu.Lock(); defer q.mu.Unlock(); return len(q.telemetry) }
