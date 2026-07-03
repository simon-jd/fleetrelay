package controlhttp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/fleetrelay/fleetrelay/internal/control"
)

type Server struct {
	Service          *control.Service
	Store            control.Store
	InternalToken    string
	DevOperatorToken string
	DevViewerToken   string
	Ready            func(context.Context) error
}

func (s Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", ok)
	m.HandleFunc("GET /readyz", s.ready)
	m.HandleFunc("GET /metrics", metrics)
	m.HandleFunc("GET /v1/devices", s.query(s.devices))
	m.HandleFunc("POST /v1/sessions", s.internal(s.session))
	m.HandleFunc("POST /v1/leases/acquire", s.mutation(s.acquire))
	m.HandleFunc("POST /v1/commands", s.mutation(s.command))
	m.HandleFunc("GET /v1/commands/{id}", s.query(s.getCommand))
	m.HandleFunc("POST /v1/commands/{id}/dispatched", s.internal(s.dispatched))
	m.HandleFunc("POST /v1/commands/{id}/ack", s.internal(s.ack))
	return m
}

type identityKey struct{}
type actor struct {
	ID   string
	Role control.Role
}

func same(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func bearer(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(value, "Bearer ")
}
func (s Server) bearerActor(r *http.Request) (actor, bool) {
	token := bearer(r)
	if s.DevOperatorToken != "" && same(token, s.DevOperatorToken) {
		return actor{"dev-operator", control.RoleOperator}, true
	}
	if s.DevViewerToken != "" && same(token, s.DevViewerToken) {
		return actor{"dev-viewer", control.RoleViewer}, true
	}
	return actor{}, false
}
func (s Server) query(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, ok := s.bearerActor(r)
		if !ok {
			writeErr(w, control.ErrForbidden)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, a)))
	}
}
func (s Server) mutation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a, ok := s.bearerActor(r); ok {
			if a.Role != control.RoleOperator {
				writeErr(w, control.ErrForbidden)
				return
			}
			next(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, a)))
			return
		}
		if s.InternalToken == "" || !same(r.Header.Get("X-FleetRelay-Internal-Token"), s.InternalToken) {
			writeErr(w, control.ErrForbidden)
			return
		}
		role := control.Role(r.Header.Get("X-FleetRelay-Actor-Role"))
		id := r.Header.Get("X-FleetRelay-Actor-ID")
		if id == "" || (role != control.RoleOperator && role != control.RoleAdmin) {
			writeErr(w, control.ErrForbidden)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, actor{id, role})))
	}
}

func ok(w http.ResponseWriter, _ *http.Request) { write(w, 200, map[string]string{"status": "ok"}) }
func metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.Write([]byte("# HELP fleetrelay_up Process readiness.\n# TYPE fleetrelay_up gauge\nfleetrelay_up 1\n"))
}
func (s Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.Ready != nil {
		if err := s.Ready(r.Context()); err != nil {
			write(w, 503, map[string]string{"status": "unavailable"})
			return
		}
	}
	ok(w, r)
}
func (s Server) internal(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-FleetRelay-Internal-Token")
		if s.InternalToken == "" || len(got) != len(s.InternalToken) || subtle.ConstantTimeCompare([]byte(got), []byte(s.InternalToken)) != 1 {
			writeErr(w, control.ErrForbidden)
			return
		}
		next(w, r)
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeErr(w, control.ErrInvalid)
		return false
	}
	return true
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func writeErr(w http.ResponseWriter, err error) {
	status, message := http.StatusInternalServerError, "internal error"
	switch {
	case errors.Is(err, control.ErrInvalid), errors.Is(err, control.ErrExpired):
		status, message = 400, err.Error()
	case errors.Is(err, control.ErrForbidden):
		status, message = 403, "forbidden"
	case errors.Is(err, control.ErrNotFound):
		status, message = 404, "not found"
	case errors.Is(err, control.ErrConflict), errors.Is(err, control.ErrStaleLease), errors.Is(err, control.ErrStaleSession), errors.Is(err, control.ErrAckConflict), errors.Is(err, control.ErrInvalidState):
		status, message = 409, err.Error()
	case errors.Is(err, control.ErrOffline):
		status, message = 422, "device is offline"
	case errors.Is(err, control.ErrUnavailable):
		status, message = 503, "dependency unavailable"
	}
	write(w, status, map[string]string{"error": message})
}
func identity(r *http.Request) (string, control.Role) {
	a, _ := r.Context().Value(identityKey{}).(actor)
	return a.ID, a.Role
}
func (s Server) devices(w http.ResponseWriter, r *http.Request) {
	v, e := s.Service.ListDevices(r.Context())
	if e != nil {
		writeErr(w, e)
		return
	}
	write(w, 200, map[string]any{"devices": v})
}
func (s Server) session(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Device    control.Device `json:"device"`
		GatewayID string         `json:"gateway_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, e := s.Service.RegisterSession(r.Context(), in.Device, in.GatewayID)
	if e != nil {
		writeErr(w, e)
		return
	}
	write(w, 201, v)
}
func (s Server) acquire(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceID   string `json:"device_id"`
		TTLSeconds *int   `json:"ttl_seconds,omitempty"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.TTLSeconds != nil && (*in.TTLSeconds < 1 || *in.TTLSeconds > 300) {
		writeErr(w, control.ErrInvalid)
		return
	}
	op, role := identity(r)
	v, e := s.Service.AcquireLease(r.Context(), role, in.DeviceID, op)
	if e != nil {
		writeErr(w, e)
		return
	}
	write(w, 201, v)
}
func (s Server) command(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceID       string                 `json:"device_id"`
		LeaseID        string                 `json:"lease_id"`
		IdempotencyKey string                 `json:"idempotency_key"`
		Type           string                 `json:"command_type"`
		Payload        json.RawMessage        `json:"payload"`
		DeliveryPolicy control.DeliveryPolicy `json:"delivery_policy"`
		ExpiresAt      time.Time              `json:"expires_at"`
	}
	if !decode(w, r, &in) {
		return
	}
	op, role := identity(r)
	v, dup, e := s.Service.CreateCommand(r.Context(), control.CreateCommandRequest{OperatorID: op, Role: role, DeviceID: in.DeviceID, LeaseID: in.LeaseID, IdempotencyKey: in.IdempotencyKey, Type: in.Type, Payload: in.Payload, DeliveryPolicy: in.DeliveryPolicy, ExpiresAt: in.ExpiresAt})
	if e != nil {
		writeErr(w, e)
		return
	}
	status := 201
	if dup {
		status = 200
	}
	write(w, status, v)
}
func (s Server) getCommand(w http.ResponseWriter, r *http.Request) {
	v, e := s.Store.GetCommand(r.Context(), r.PathValue("id"))
	if e != nil {
		writeErr(w, e)
		return
	}
	write(w, 200, v)
}
func (s Server) dispatched(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceID   string `json:"device_id"`
		GatewayID  string `json:"gateway_id"`
		Generation uint64 `json:"session_generation"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, dup, e := s.Service.MarkDispatched(r.Context(), control.DispatchReceipt{CommandID: r.PathValue("id"), DeviceID: in.DeviceID, GatewayID: in.GatewayID, SessionGeneration: in.Generation})
	if e != nil {
		writeErr(w, e)
		return
	}
	write(w, 200, map[string]any{"command": v, "duplicate": dup})
}
func (s Server) ack(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceID       string                `json:"device_id"`
		GatewayID      string                `json:"gateway_id"`
		Generation     uint64                `json:"session_generation"`
		LeaseEpoch     uint64                `json:"lease_epoch"`
		DeviceSequence uint64                `json:"device_sequence"`
		Result         control.CommandStatus `json:"result"`
		Detail         string                `json:"detail"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, dup, e := s.Service.Acknowledge(r.Context(), control.Ack{CommandID: r.PathValue("id"), DeviceID: in.DeviceID, GatewayID: in.GatewayID, SessionGeneration: in.Generation, LeaseEpoch: in.LeaseEpoch, DeviceSequence: in.DeviceSequence, Result: in.Result, Detail: in.Detail})
	if e != nil {
		writeErr(w, e)
		return
	}
	write(w, 200, map[string]any{"command": v, "duplicate": dup})
}
