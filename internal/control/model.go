package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	ErrConflict     = errors.New("control lease is held by another operator")
	ErrForbidden    = errors.New("operation is not permitted")
	ErrInvalid      = errors.New("invalid request")
	ErrNotFound     = errors.New("resource not found")
	ErrOffline      = errors.New("device is offline")
	ErrStaleLease   = errors.New("stale control lease")
	ErrStaleSession = errors.New("stale device session")
	ErrExpired      = errors.New("command has expired")
	ErrInvalidState = errors.New("invalid command state transition")
	ErrUnavailable  = errors.New("dependency unavailable")
	ErrAckConflict  = errors.New("ack conflicts with terminal result")
)

type CommandStatus string

const (
	StatusRejected         CommandStatus = "REJECTED"
	StatusAccepted         CommandStatus = "ACCEPTED"
	StatusPendingTransport CommandStatus = "PENDING_TRANSPORT"
	StatusDispatched       CommandStatus = "DISPATCHED"
	StatusSucceeded        CommandStatus = "SUCCEEDED"
	StatusFailed           CommandStatus = "FAILED"
	StatusAckTimeout       CommandStatus = "ACK_TIMEOUT"
	StatusExpired          CommandStatus = "EXPIRED"
	StatusCancelled        CommandStatus = "CANCELLED"
)

func (s CommandStatus) TerminalForOrdering() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusExpired || s == StatusCancelled
}

func CanTransition(from, to CommandStatus) bool {
	switch from {
	case StatusAccepted:
		return to == StatusPendingTransport || to == StatusRejected
	case StatusPendingTransport:
		return to == StatusDispatched || to == StatusExpired || to == StatusCancelled
	case StatusDispatched:
		return to == StatusSucceeded || to == StatusFailed || to == StatusAckTimeout
	case StatusAckTimeout:
		// A late application ACK is reconciliation, not a second execution.
		return to == StatusSucceeded || to == StatusFailed
	default:
		return false
	}
}

type DeliveryPolicy string

const (
	RequireOnline DeliveryPolicy = "REQUIRE_ONLINE"
	QueueUntil    DeliveryPolicy = "QUEUE_UNTIL"
)

type Role string

const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
)

type Device struct {
	ID           string    `json:"device_id"`
	DisplayName  string    `json:"display_name"`
	AdapterType  string    `json:"adapter_type"`
	Capabilities []string  `json:"capabilities"`
	LastSeenAt   time.Time `json:"last_seen_at"`
	State        string    `json:"state"`
}

type DeviceSession struct {
	ID         string    `json:"session_id"`
	DeviceID   string    `json:"device_id"`
	GatewayID  string    `json:"gateway_id"`
	Generation uint64    `json:"session_generation"`
	StartedAt  time.Time `json:"started_at"`
}

type Lease struct {
	ID         string    `json:"lease_id"`
	DeviceID   string    `json:"device_id"`
	OperatorID string    `json:"operator_id"`
	Epoch      uint64    `json:"lease_epoch"`
	ExpiresAt  time.Time `json:"expires_at"`
	Status     string    `json:"status"`
}

func (l Lease) Active(now time.Time) bool { return l.Status == "ACTIVE" && l.ExpiresAt.After(now) }

type Command struct {
	ID             string          `json:"command_id"`
	IdempotencyKey string          `json:"idempotency_key"`
	OperatorID     string          `json:"operator_id"`
	DeviceID       string          `json:"device_id"`
	DeviceSequence uint64          `json:"device_sequence"`
	LeaseID        string          `json:"lease_id"`
	LeaseEpoch     uint64          `json:"lease_epoch"`
	Type           string          `json:"command_type"`
	Payload        json.RawMessage `json:"payload"`
	DeliveryPolicy DeliveryPolicy  `json:"delivery_policy"`
	ExpiresAt      time.Time       `json:"expires_at"`
	Status         CommandStatus   `json:"status"`
	ResultDetail   string          `json:"result_detail,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type CommandAttempt struct {
	CommandID               string    `json:"command_id"`
	Attempt                 uint32    `json:"attempt"`
	TargetGatewayID         string    `json:"target_gateway_id"`
	TargetSessionGeneration uint64    `json:"target_session_generation"`
	RoutedAt                time.Time `json:"routed_at"`
	Deadline                time.Time `json:"attempt_deadline"`
}

type OutboxItem struct {
	ID                  string          `json:"outbox_id"`
	AggregateID         string          `json:"aggregate_id"`
	Kind                string          `json:"kind"`
	Payload             json.RawMessage `json:"payload"`
	CreatedAt           time.Time       `json:"created_at"`
	PublicationFailures int             `json:"publication_failures"`
}

// Delivery is an immutable transport intent, committed with its attempt.
// The enclosing outbox ID is the stable delivery ID across Redis retries.
type Delivery struct {
	Command Command        `json:"command"`
	Attempt CommandAttempt `json:"attempt"`
}

type RetryCandidate struct {
	Command      Command
	LastAttempt  CommandAttempt
	AttemptCount uint32
}

type CreateCommandRequest struct {
	OperatorID     string
	Role           Role
	DeviceID       string
	LeaseID        string
	IdempotencyKey string
	Type           string
	Payload        json.RawMessage
	DeliveryPolicy DeliveryPolicy
	ExpiresAt      time.Time
}

type Ack struct {
	CommandID         string
	DeviceID          string
	GatewayID         string
	SessionGeneration uint64
	LeaseEpoch        uint64
	DeviceSequence    uint64
	Result            CommandStatus
	Detail            string
}

type DispatchReceipt struct {
	CommandID         string
	DeviceID          string
	GatewayID         string
	SessionGeneration uint64
}

func ValidateTransition(from, to CommandStatus) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidState, from, to)
	}
	return nil
}
