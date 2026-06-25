package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/fleetrelay/fleetrelay/internal/control"
	"github.com/google/uuid"
)

// Store is a deterministic, concurrency-safe reference implementation used by
// unit tests and single-process demos. Production binaries use postgres.Store.
type Store struct {
	mu          sync.Mutex
	devices     map[string]control.Device
	sessions    map[string]control.DeviceSession
	currentGen  map[string]uint64
	leases      map[string]control.Lease
	leaseByDev  map[string]string
	leaseEpoch  map[string]uint64
	commands    map[string]control.Command
	idempotency map[string]string
	sequence    map[string]uint64
	attempts    map[string][]control.CommandAttempt
	outbox      map[string]control.OutboxItem
	published   map[string]bool
}

func New() *Store {
	return &Store{
		devices: make(map[string]control.Device), sessions: make(map[string]control.DeviceSession),
		currentGen: make(map[string]uint64), leases: make(map[string]control.Lease), leaseByDev: make(map[string]string),
		leaseEpoch: make(map[string]uint64), commands: make(map[string]control.Command),
		idempotency: make(map[string]string), sequence: make(map[string]uint64), attempts: make(map[string][]control.CommandAttempt),
		outbox: make(map[string]control.OutboxItem), published: make(map[string]bool),
	}
}

func (s *Store) Ping(context.Context) error { return nil }

func (s *Store) ListDevices(_ context.Context) ([]control.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]control.Device, 0, len(s.devices))
	for _, device := range s.devices {
		out = append(out, device)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) appendOutbox(kind, aggregate string, value any, now time.Time) {
	b, _ := json.Marshal(value)
	id := uuid.NewString()
	s.outbox[id] = control.OutboxItem{ID: id, AggregateID: aggregate, Kind: kind, Payload: b, CreatedAt: now}
}

func (s *Store) RegisterSession(_ context.Context, d control.Device, gatewayID string, now time.Time) (control.DeviceSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentGen[d.ID]++
	d.LastSeenAt = now
	s.devices[d.ID] = d
	sess := control.DeviceSession{ID: uuid.NewString(), DeviceID: d.ID, GatewayID: gatewayID, Generation: s.currentGen[d.ID], StartedAt: now}
	s.sessions[sess.ID] = sess
	s.appendOutbox("device.session.started", d.ID, sess, now)
	return sess, nil
}

func (s *Store) AcquireLease(_ context.Context, deviceID, operatorID string, ttl time.Duration, now time.Time) (control.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.devices[deviceID]; !ok {
		return control.Lease{}, control.ErrNotFound
	}
	if id := s.leaseByDev[deviceID]; id != "" {
		old := s.leases[id]
		if old.Active(now) {
			if old.OperatorID == operatorID {
				return old, nil
			}
			return control.Lease{}, control.ErrConflict
		}
		old.Status = "EXPIRED"
		s.leases[id] = old
	}
	s.leaseEpoch[deviceID]++
	l := control.Lease{ID: uuid.NewString(), DeviceID: deviceID, OperatorID: operatorID, Epoch: s.leaseEpoch[deviceID], ExpiresAt: now.Add(ttl), Status: "ACTIVE"}
	s.leases[l.ID] = l
	s.leaseByDev[deviceID] = l.ID
	s.appendOutbox("lease.changed", deviceID, l, now)
	return l, nil
}

func (s *Store) RenewLease(_ context.Context, leaseID, operatorID string, ttl time.Duration, now time.Time) (control.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[leaseID]
	if !ok {
		return control.Lease{}, control.ErrNotFound
	}
	if l.OperatorID != operatorID || !l.Active(now) {
		return control.Lease{}, control.ErrStaleLease
	}
	l.ExpiresAt = now.Add(ttl)
	s.leases[leaseID] = l
	s.appendOutbox("lease.changed", l.DeviceID, l, now)
	return l, nil
}

func (s *Store) ReleaseLease(_ context.Context, leaseID, operatorID string, now time.Time) (control.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[leaseID]
	if !ok {
		return control.Lease{}, control.ErrNotFound
	}
	if l.OperatorID != operatorID || l.Status != "ACTIVE" {
		return control.Lease{}, control.ErrStaleLease
	}
	l.Status = "RELEASED"
	l.ExpiresAt = now
	s.leases[leaseID] = l
	s.appendOutbox("lease.changed", l.DeviceID, l, now)
	return l, nil
}

func (s *Store) CreateCommand(_ context.Context, req control.CreateCommandRequest, now time.Time) (control.Command, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := req.OperatorID + "\x00" + req.DeviceID + "\x00" + req.IdempotencyKey
	if id := s.idempotency[key]; id != "" {
		return s.commands[id], true, nil
	}
	l, ok := s.leases[req.LeaseID]
	if !ok || l.DeviceID != req.DeviceID || l.OperatorID != req.OperatorID || !l.Active(now) {
		return control.Command{}, false, control.ErrStaleLease
	}
	d := s.devices[req.DeviceID]
	found := false
	for _, capability := range d.Capabilities {
		if capability == req.Type {
			found = true
			break
		}
	}
	if !found {
		return control.Command{}, false, fmt.Errorf("%w: unsupported capability %s", control.ErrInvalid, req.Type)
	}
	s.sequence[req.DeviceID]++
	c := control.Command{ID: uuid.NewString(), IdempotencyKey: req.IdempotencyKey, OperatorID: req.OperatorID, DeviceID: req.DeviceID,
		DeviceSequence: s.sequence[req.DeviceID], LeaseID: l.ID, LeaseEpoch: l.Epoch, Type: req.Type, Payload: append(json.RawMessage(nil), req.Payload...),
		DeliveryPolicy: req.DeliveryPolicy, ExpiresAt: req.ExpiresAt, Status: control.StatusPendingTransport, CreatedAt: now, UpdatedAt: now}
	s.commands[c.ID] = c
	s.idempotency[key] = c.ID
	s.appendOutbox("command.created", c.ID, c, now)
	return c, false, nil
}

func (s *Store) GetCommand(_ context.Context, id string) (control.Command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.commands[id]
	if !ok {
		return control.Command{}, control.ErrNotFound
	}
	return c, nil
}

func (s *Store) TransitionCommand(_ context.Context, id string, to control.CommandStatus, detail string, now time.Time) (control.Command, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.commands[id]
	if !ok {
		return control.Command{}, false, control.ErrNotFound
	}
	if c.Status == to {
		return c, true, nil
	}
	if err := control.ValidateTransition(c.Status, to); err != nil {
		return c, false, err
	}
	if (to == control.StatusDispatched || to == control.StatusSucceeded) && !c.ExpiresAt.After(now) {
		return c, false, control.ErrExpired
	}
	c.Status = to
	c.ResultDetail = detail
	c.UpdatedAt = now
	s.commands[id] = c
	s.appendOutbox("command.status", id, c, now)
	return c, false, nil
}

func (s *Store) MarkDispatched(_ context.Context, receipt control.DispatchReceipt, now time.Time) (control.Command, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.commands[receipt.CommandID]
	if !ok {
		return control.Command{}, false, control.ErrNotFound
	}
	if c.DeviceID != receipt.DeviceID || s.currentGen[c.DeviceID] != receipt.SessionGeneration {
		return c, false, control.ErrStaleSession
	}
	attempts := s.attempts[c.ID]
	if len(attempts) == 0 {
		return c, false, control.ErrInvalidState
	}
	a := attempts[len(attempts)-1]
	if a.TargetGatewayID != receipt.GatewayID || a.TargetSessionGeneration != receipt.SessionGeneration {
		return c, false, control.ErrStaleSession
	}
	if c.Status == control.StatusDispatched || c.Status == control.StatusSucceeded || c.Status == control.StatusFailed {
		return c, true, nil
	}
	if !c.ExpiresAt.After(now) {
		return c, false, control.ErrExpired
	}
	if err := control.ValidateTransition(c.Status, control.StatusDispatched); err != nil {
		return c, false, err
	}
	c.Status = control.StatusDispatched
	c.UpdatedAt = now
	s.commands[c.ID] = c
	s.appendOutbox("command.status", c.ID, c, now)
	return c, false, nil
}

func (s *Store) Acknowledge(_ context.Context, ack control.Ack, now time.Time) (control.Command, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.commands[ack.CommandID]
	if !ok {
		return control.Command{}, false, control.ErrNotFound
	}
	if c.DeviceID != ack.DeviceID || c.LeaseEpoch != ack.LeaseEpoch || c.DeviceSequence != ack.DeviceSequence {
		return c, false, control.ErrInvalid
	}
	if s.currentGen[c.DeviceID] != ack.SessionGeneration {
		return c, false, control.ErrStaleSession
	}
	attempts := s.attempts[c.ID]
	if len(attempts) == 0 {
		return c, false, control.ErrInvalidState
	}
	a := attempts[len(attempts)-1]
	if a.TargetGatewayID != ack.GatewayID || a.TargetSessionGeneration != ack.SessionGeneration {
		return c, false, control.ErrStaleSession
	}
	if c.Status == ack.Result {
		return c, true, nil
	}
	if c.Status == control.StatusSucceeded || c.Status == control.StatusFailed {
		return c, false, control.ErrAckConflict
	}
	if c.Status != control.StatusPendingTransport {
		if err := control.ValidateTransition(c.Status, ack.Result); err != nil {
			return c, false, err
		}
	} else if !c.ExpiresAt.After(now) {
		return c, false, control.ErrExpired
	}
	if ack.Result != control.StatusSucceeded && ack.Result != control.StatusFailed {
		return c, false, control.ErrInvalid
	}
	c.Status = ack.Result
	c.ResultDetail = ack.Detail
	c.UpdatedAt = now
	s.commands[c.ID] = c
	s.appendOutbox("command.status", c.ID, c, now)
	return c, false, nil
}

func (s *Store) CreateAttempt(_ context.Context, commandID, gateway string, generation uint64, routed, deadline time.Time) (control.CommandAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.commands[commandID]
	if !ok {
		return control.CommandAttempt{}, control.ErrNotFound
	}
	if !c.ExpiresAt.After(routed) {
		return control.CommandAttempt{}, control.ErrExpired
	}
	l := s.leases[c.LeaseID]
	if !l.Active(routed) || l.Epoch != c.LeaseEpoch {
		return control.CommandAttempt{}, control.ErrStaleLease
	}
	if c.Status != control.StatusPendingTransport && c.Status != control.StatusDispatched {
		return control.CommandAttempt{}, control.ErrInvalidState
	}
	if current := s.currentGen[c.DeviceID]; current != generation {
		return control.CommandAttempt{}, control.ErrStaleSession
	}
	matchedGateway := false
	for _, sess := range s.sessions {
		if sess.DeviceID == c.DeviceID && sess.Generation == generation && sess.GatewayID == gateway {
			matchedGateway = true
			break
		}
	}
	if !matchedGateway {
		return control.CommandAttempt{}, control.ErrStaleSession
	}
	for _, other := range s.commands {
		if other.DeviceID == c.DeviceID && other.ID != c.ID && !other.Status.TerminalForOrdering() && other.DeviceSequence < c.DeviceSequence {
			return control.CommandAttempt{}, control.ErrConflict
		}
	}
	a := control.CommandAttempt{CommandID: commandID, Attempt: uint32(len(s.attempts[commandID]) + 1), TargetGatewayID: gateway, TargetSessionGeneration: generation, RoutedAt: routed, Deadline: deadline}
	s.attempts[commandID] = append(s.attempts[commandID], a)
	return a, nil
}

func (s *Store) PrepareOutboxCommand(_ context.Context, id string, now time.Time) (control.Command, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.commands[id]
	if !ok {
		return c, false, control.ErrNotFound
	}
	if c.Status != control.StatusPendingTransport {
		return c, true, nil
	}
	to := control.CommandStatus("")
	if !c.ExpiresAt.After(now) {
		to = control.StatusExpired
	} else {
		l := s.leases[c.LeaseID]
		if !l.Active(now) || l.Epoch != c.LeaseEpoch {
			to = control.StatusCancelled
		}
	}
	if to == "" {
		return c, false, nil
	}
	c.Status = to
	c.UpdatedAt = now
	s.commands[id] = c
	s.appendOutbox("command.status", id, c, now)
	return c, true, nil
}

func (s *Store) ListRetryCandidates(_ context.Context, now time.Time, limit int) ([]control.RetryCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []control.RetryCandidate
	for id, attempts := range s.attempts {
		if len(attempts) == 0 {
			continue
		}
		c := s.commands[id]
		last := attempts[len(attempts)-1]
		if (c.Status == control.StatusPendingTransport || c.Status == control.StatusDispatched) && !last.Deadline.After(now) {
			out = append(out, control.RetryCandidate{Command: c, LastAttempt: last, AttemptCount: uint32(len(attempts))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAttempt.Deadline.Before(out[j].LastAttempt.Deadline) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) ResolveAttemptDeadline(_ context.Context, id string, max uint32, now time.Time) (control.Command, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.commands[id]
	if !ok {
		return c, false, control.ErrNotFound
	}
	attempts := s.attempts[id]
	to := control.CommandStatus("")
	if c.Status == control.StatusPendingTransport && !c.ExpiresAt.After(now) {
		to = control.StatusExpired
	}
	if c.Status == control.StatusDispatched && (!c.ExpiresAt.After(now) || uint32(len(attempts)) >= max) {
		to = control.StatusAckTimeout
	}
	if to == "" {
		return c, false, nil
	}
	if err := control.ValidateTransition(c.Status, to); err != nil {
		return c, false, err
	}
	c.Status = to
	c.UpdatedAt = now
	s.commands[id] = c
	s.appendOutbox("command.status", id, c, now)
	return c, true, nil
}

func (s *Store) ClaimOutbox(_ context.Context, limit int) ([]control.OutboxItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []control.OutboxItem
	for id, item := range s.outbox {
		if !s.published[id] {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) MarkOutboxPublished(_ context.Context, id string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.outbox[id]; !ok {
		return control.ErrNotFound
	}
	s.published[id] = true
	return nil
}
