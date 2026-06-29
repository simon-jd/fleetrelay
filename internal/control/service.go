package control

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type Presence func(context.Context, string) (bool, error)

type Service struct {
	store    Store
	presence Presence
	now      func() time.Time
	leaseTTL time.Duration
}

func NewService(store Store, presence Presence, leaseTTL time.Duration) *Service {
	if presence == nil {
		presence = func(context.Context, string) (bool, error) { return true, nil }
	}
	if leaseTTL <= 0 {
		leaseTTL = 30 * time.Second
	}
	return &Service{store: store, presence: presence, now: time.Now, leaseTTL: leaseTTL}
}

func (s *Service) WithClock(now func() time.Time) *Service { s.now = now; return s }

func (s *Service) ListDevices(ctx context.Context) ([]Device, error) {
	devices, err := s.store.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	for i := range devices {
		online, presenceErr := s.presence(ctx, devices[i].ID)
		if presenceErr != nil {
			return nil, fmt.Errorf("%w: presence: %v", ErrUnavailable, presenceErr)
		}
		devices[i].State = "OFFLINE"
		if online {
			devices[i].State = "ONLINE"
		}
	}
	return devices, nil
}

func (s *Service) RegisterSession(ctx context.Context, device Device, gatewayID string) (DeviceSession, error) {
	if device.ID == "" || gatewayID == "" {
		return DeviceSession{}, fmt.Errorf("%w: device_id and gateway_id are required", ErrInvalid)
	}
	return s.store.RegisterSession(ctx, device, gatewayID, s.now())
}

func (s *Service) AcquireLease(ctx context.Context, role Role, deviceID, operatorID string) (Lease, error) {
	if role != RoleOperator && role != RoleAdmin {
		return Lease{}, ErrForbidden
	}
	if deviceID == "" || operatorID == "" {
		return Lease{}, ErrInvalid
	}
	return s.store.AcquireLease(ctx, deviceID, operatorID, s.leaseTTL, s.now())
}

func (s *Service) RenewLease(ctx context.Context, role Role, leaseID, operatorID string) (Lease, error) {
	if role != RoleOperator && role != RoleAdmin {
		return Lease{}, ErrForbidden
	}
	return s.store.RenewLease(ctx, leaseID, operatorID, s.leaseTTL, s.now())
}

func (s *Service) ReleaseLease(ctx context.Context, role Role, leaseID, operatorID string) (Lease, error) {
	if role != RoleOperator && role != RoleAdmin {
		return Lease{}, ErrForbidden
	}
	return s.store.ReleaseLease(ctx, leaseID, operatorID, s.now())
}

func (s *Service) CreateCommand(ctx context.Context, req CreateCommandRequest) (Command, bool, error) {
	now := s.now()
	if req.Role != RoleOperator && req.Role != RoleAdmin {
		return Command{}, false, ErrForbidden
	}
	if req.DeviceID == "" || req.OperatorID == "" || req.LeaseID == "" || req.IdempotencyKey == "" || req.Type == "" {
		return Command{}, false, ErrInvalid
	}
	if req.ExpiresAt.IsZero() || !req.ExpiresAt.After(now) {
		return Command{}, false, ErrExpired
	}
	if len(req.Payload) == 0 {
		req.Payload = json.RawMessage(`{}`)
	}
	if !json.Valid(req.Payload) {
		return Command{}, false, fmt.Errorf("%w: payload must be JSON", ErrInvalid)
	}
	if req.DeliveryPolicy == "" {
		req.DeliveryPolicy = RequireOnline
	}
	if req.DeliveryPolicy != RequireOnline && req.DeliveryPolicy != QueueUntil {
		return Command{}, false, ErrInvalid
	}
	if req.DeliveryPolicy == RequireOnline {
		online, err := s.presence(ctx, req.DeviceID)
		if err != nil {
			return Command{}, false, fmt.Errorf("%w: presence: %v", ErrUnavailable, err)
		}
		if !online {
			return Command{}, false, ErrOffline
		}
	}
	return s.store.CreateCommand(ctx, req, now)
}

func (s *Service) MarkDispatched(ctx context.Context, receipt DispatchReceipt) (Command, bool, error) {
	if receipt.CommandID == "" || receipt.DeviceID == "" || receipt.GatewayID == "" || receipt.SessionGeneration == 0 {
		return Command{}, false, ErrInvalid
	}
	return s.store.MarkDispatched(ctx, receipt, s.now())
}

func (s *Service) Acknowledge(ctx context.Context, ack Ack) (Command, bool, error) {
	if ack.CommandID == "" || ack.DeviceID == "" || ack.GatewayID == "" || ack.SessionGeneration == 0 || ack.LeaseEpoch == 0 || ack.DeviceSequence == 0 {
		return Command{}, false, ErrInvalid
	}
	if ack.Result != StatusSucceeded && ack.Result != StatusFailed {
		return Command{}, false, ErrInvalid
	}
	return s.store.Acknowledge(ctx, ack, s.now())
}
