# Operator Console Integration Contract

The V1 design specifies message names and envelope semantics but not concrete HTTP paths. The console and Compose topology use the following minimal integration surface so the Go services and UI can evolve independently.

## HTTP

- `GET /api/v1/devices` returns either a device array or `{ "devices": [...] }`.
- `GET /healthz` reports process liveness.
- `GET /readyz` reports whether the service can perform its authoritative role.
- `GET /metrics` exposes Prometheus metrics.

## WebSocket

The browser opens `/ws`, then sends `authenticate`, `subscribe`, and optionally `resume`. Every frame is a JSON object with:

```json
{
  "schema_version": 1,
  "message_id": "a UUID or ULID",
  "type": "command.request",
  "device_id": "vacuum-floor-2",
  "sent_at": "2026-08-31T20:00:00Z",
  "payload": {}
}
```

The console consumes `snapshot`, `device.event`, `telemetry.update`, `lease.changed`, `command.status`, `error`, and `resume.complete`. The current Gateway returns a fresh snapshot on subscription and acknowledges `resume` with zero replayed events; it does not query a durable event log. Live messages, snapshot refresh, or HTTP status polling are not ordered, lossless replay and may omit transitions that occurred while disconnected. `event_id` and opaque `event_cursor` remain prospective fields for a future durable implementation. A slow consumer may be closed with code 1013 and reconnect, but must not report the missing interval as replayed.

Lease acquisition sends `{ "ttl_seconds": 30 }`. A command request sends `command_type`, a unique retry-stable `idempotency_key`, `lease_id`, `delivery_policy`, server-checked `expires_at`, and `payload`. Server authorization and capability validation remain authoritative.

The console accepts a static development token through its token field. It sends Bearer syntax on HTTP reads and the raw token in the WebSocket `authenticate` payload. The current local stack does not validate that value as a JWT or establish a production trust boundary. Production deployments require a short-lived, authoritatively verified credential from an external identity flow plus enforced authorization and encrypted transport; credentials must not be embedded at image build time.
