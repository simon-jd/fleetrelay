CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE devices (
  device_id text PRIMARY KEY,
  display_name text NOT NULL DEFAULT '',
  adapter_type text NOT NULL,
  capabilities jsonb NOT NULL DEFAULT '[]',
  desired_status text NOT NULL DEFAULT 'ACTIVE',
  last_seen_at timestamptz,
  version bigint NOT NULL DEFAULT 1
);
CREATE TABLE device_sessions (
  session_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  device_id text NOT NULL REFERENCES devices(device_id),
  gateway_id text NOT NULL,
  session_generation bigint NOT NULL CHECK (session_generation > 0),
  started_at timestamptz NOT NULL,
  ended_at timestamptz,
  UNIQUE(device_id, session_generation)
);
CREATE TABLE control_leases (
  lease_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  device_id text NOT NULL REFERENCES devices(device_id),
  operator_id text NOT NULL,
  lease_epoch bigint NOT NULL CHECK (lease_epoch > 0),
  expires_at timestamptz NOT NULL,
  status text NOT NULL CHECK (status IN ('ACTIVE','RELEASED','EXPIRED','REVOKED')),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(device_id, lease_epoch)
);
CREATE UNIQUE INDEX one_active_lease_per_device ON control_leases(device_id) WHERE status = 'ACTIVE';
CREATE TABLE device_counters (
  device_id text PRIMARY KEY REFERENCES devices(device_id),
  next_sequence bigint NOT NULL DEFAULT 1,
  next_lease_epoch bigint NOT NULL DEFAULT 1
);
CREATE TABLE commands (
  command_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  operator_id text NOT NULL,
  device_id text NOT NULL REFERENCES devices(device_id),
  idempotency_key text NOT NULL,
  device_sequence bigint NOT NULL CHECK (device_sequence > 0),
  lease_id uuid NOT NULL REFERENCES control_leases(lease_id),
  lease_epoch bigint NOT NULL,
  command_type text NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}',
  delivery_policy text NOT NULL CHECK (delivery_policy IN ('REQUIRE_ONLINE','QUEUE_UNTIL')),
  expires_at timestamptz NOT NULL,
  status text NOT NULL,
  result_detail text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(operator_id, device_id, idempotency_key),
  UNIQUE(device_id, device_sequence)
);
CREATE TABLE command_attempts (
  command_id uuid NOT NULL REFERENCES commands(command_id),
  attempt integer NOT NULL CHECK (attempt > 0),
  target_gateway_id text NOT NULL,
  target_session_generation bigint NOT NULL,
  routed_at timestamptz NOT NULL,
  attempt_deadline timestamptz NOT NULL,
  PRIMARY KEY(command_id, attempt)
);
CREATE TABLE event_cursor (singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton), next_offset bigint NOT NULL);
INSERT INTO event_cursor(singleton, next_offset) VALUES (true, 1);
CREATE TABLE domain_events (
  event_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  event_offset bigint UNIQUE NOT NULL,
  aggregate_id text NOT NULL,
  kind text NOT NULL,
  payload jsonb NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE audit_records (
  audit_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  actor_id text NOT NULL,
  action text NOT NULL,
  target_id text NOT NULL,
  request_id text NOT NULL DEFAULT '',
  result text NOT NULL,
  context jsonb NOT NULL DEFAULT '{}',
  occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE outbox (
  outbox_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  aggregate_id text NOT NULL,
  kind text NOT NULL,
  payload jsonb NOT NULL,
  business_key text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz,
  projected_at timestamptz
);
CREATE INDEX outbox_pending ON outbox(created_at) WHERE published_at IS NULL;
