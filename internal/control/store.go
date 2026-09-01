package control

import (
	"context"
	"time"
)

type Store interface {
	Ping(context.Context) error
	ListDevices(context.Context) ([]Device, error)
	RegisterSession(context.Context, Device, string, time.Time) (DeviceSession, error)
	AcquireLease(context.Context, string, string, time.Duration, time.Time) (Lease, error)
	RenewLease(context.Context, string, string, time.Duration, time.Time) (Lease, error)
	ReleaseLease(context.Context, string, string, time.Time) (Lease, error)
	CreateCommand(context.Context, CreateCommandRequest, time.Time) (Command, bool, error)
	GetCommand(context.Context, string) (Command, error)
	TransitionCommand(context.Context, string, CommandStatus, string, time.Time) (Command, bool, error)
	MarkDispatched(context.Context, DispatchReceipt, time.Time) (Command, bool, error)
	Acknowledge(context.Context, Ack, time.Time) (Command, bool, error)
	CreateAttempt(context.Context, string, string, uint64, time.Time, time.Time) (CommandAttempt, error)
	PrepareOutboxCommand(context.Context, string, time.Time) (Command, bool, error)
	ListRetryCandidates(context.Context, time.Time, int) ([]RetryCandidate, error)
	ResolveAttemptDeadline(context.Context, string, uint32, time.Time) (Command, bool, error)
	ClaimOutbox(context.Context, int) ([]OutboxItem, error)
	MarkOutboxPublished(context.Context, string, time.Time) error
}
