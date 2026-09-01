# FleetRelay Local Operations Runbook

This runbook covers the partial Docker Compose prototype. It does not describe a production deployment or promise measured performance, reliability, security, or complete observability. Failure behavior below is explicitly labeled either current behavior or a design acceptance target.

Current limitations: durable browser replay, complete command retry/expiry/ACK-timeout reconciliation, JWT verification, production authorization, service authentication, TLS/device mTLS, and Go OpenTelemetry instrumentation are not implemented. WebSocket reconnect, a fresh snapshot, live status updates, and any current-state polling are recovery aids, not an ordered or lossless event replay. The collector and dashboards are scaffolding, not evidence that an end-to-end trace or reliability objective has been met.

## Service topology and health

| Service | Internal ports | Healthy when | Dependency failure behavior |
|---|---:|---|---|
| `control-api` | HTTP `8080` | `/healthz` and `/readyz` currently report process health; dependency-aware readiness is pending | PostgreSQL-backed mutations return errors when the database is unavailable |
| `gateway-a`, `gateway-b` | HTTP/WS `8081`, device gRPC `9090` | process is live; readiness can reach Redis | Connected streams end when their Gateway stops; automatic cross-Gateway recovery is not yet implemented |
| `dispatcher` | configured metrics `8082` | no dedicated health endpoint is implemented; inspect process state and logs | Commands already committed with outbox rows remain in PostgreSQL |
| `device-sim` | none | three device sessions are visible | reconnects to its configured Gateway with jitter; it does not select an alternate Gateway |
| `postgres` | `5432` | `pg_isready` succeeds | no authoritative mutations are accepted |
| `redis` | `6379` | `PING` returns `PONG` | realtime transport pauses; durable state is unchanged |
| `otel-collector` | OTLP `4317`, `4318` | collector configuration loads and its endpoints listen | Go services do not currently emit OTLP application telemetry |
| `prometheus` | host `9092` | targets page reports configured scrapes | most application-specific metrics are not instrumented yet |
| `grafana` | host `3001` | UI loads provisioned dashboards | application correctness is unaffected |

Liveness means the process can answer. Production readiness must prove that the service can safely perform its authoritative role. The prototype's Control API readiness handler is not dependency-aware, so do not treat it as a production routing signal.

## Start and verify

```bash
cp deploy/compose/.env.example deploy/compose/.env
docker compose --env-file deploy/compose/.env -f deploy/compose/docker-compose.yml up --build -d
docker compose -f deploy/compose/docker-compose.yml ps
curl --fail http://localhost:8080/readyz
curl --fail http://localhost:8081/readyz
curl --fail http://localhost:8083/readyz
curl --fail http://localhost:9092/-/ready
```

Prototype startup checks (not reliability evidence):

- Three devices are visible and converge to `ONLINE`.
- Both Gateways are healthy; each device is owned by exactly one current session generation.
- The Dispatcher process remains running while it polls the outbox.
- Prometheus reports the endpoints that expose placeholder process metrics as reachable.
- The Fleet Overview, Command Delivery, Connection Health, and Failure Injection dashboards are provisioned.

Inspect structured logs without following indefinitely:

```bash
docker compose -f deploy/compose/docker-compose.yml logs --since=10m control-api gateway-a gateway-b dispatcher device-sim
```

## Normal control acceptance walkthrough

This is the intended end-to-end demonstration, not a claim that every console, audit, status, and dashboard step currently works. Use it as an acceptance checklist while the prototype gaps listed above remain open.

1. Open <http://localhost:3000> and select `vacuum-floor-2`.
2. Acquire its 30-second control lease.
3. Send `PAUSE_TASK` using the console.
4. Verify the durable status sequence: `ACCEPTED`, `PENDING_TRANSPORT`, `DISPATCHED`, and `SUCCEEDED` or `FAILED`.
5. Verify that the Command Delivery dashboard shows the attempt and ACK latency.
6. Confirm the audit view associates the operator, device, lease epoch, command ID, and result without exposing the token.

A WebSocket response is not proof of device execution. Only a valid application ACK can transition a command to `SUCCEEDED` or `FAILED`.

## Failure demonstrations

Run one failure at a time against a healthy stack. The helper always attempts to restart the stopped service, including after interruption.

### Gateway failure and session fencing

```bash
sh scripts/demo-failures.sh gateway
```

Design acceptance target (not implemented or verified end to end):

- Connections on `gateway-a` close and affected devices briefly report offline or reconnecting.
- Devices reconnect through `gateway-b`; new sessions have a higher `session_generation`.
- A late send from Gateway A is rejected as a stale session.
- Durable browser events would resume using the last cursor; the current Gateway does not implement this replay path.
- 95% reconnect within 15 seconds is a target that must be measured, not assumed.

### Redis degradation

```bash
sh scripts/demo-failures.sh redis
```

Design acceptance target (only durable outbox storage and basic republishing are partially implemented):

- Already accepted commands remain in PostgreSQL and may remain `PENDING_TRANSPORT`.
- New `REQUIRE_ONLINE` commands return `UNAVAILABLE` because presence cannot be trusted.
- No command is reported as dispatched merely because an outbox row exists.
- After Redis recovers, the Dispatcher republishes stable event and command identities; consumers deduplicate them.

### PostgreSQL fail-closed behavior

```bash
sh scripts/demo-failures.sh postgres
```

Design acceptance target (not verified end to end):

- New leases, commands, and authoritative status transitions return unavailable.
- Redis data is not promoted to authoritative control state.
- After recovery, clients retry with the same idempotency key and obtain either the existing command or one new command.

Machine-readable results are stored in `docs/evidence/failures`. A `dependency_recovered: true` result verifies only that the stopped dependency became reachable again. The current prototype does not yet emit enough audit, replay, trace, and application-metric evidence to establish all correctness assertions.

## Alert response

### `FleetRelayTargetDown`

1. Identify the target and check its recent logs.
2. Check its direct dependencies before restarting it.
3. For a Gateway, expect clients to reconnect elsewhere. Do not manually rewrite session generations.
4. For Control API or PostgreSQL, preserve fail-closed behavior and avoid replaying operator requests with new idempotency keys.

### `FleetRelayOutboxBacklog`

1. Check Dispatcher health and PostgreSQL connectivity.
2. Check Redis health and `redis_consumer_lag`.
3. Compare oldest outbox age with command expiry times.
4. Restore the dependency; do not delete unpublished rows. Stable event IDs make republishing safe.

### `FleetRelayCommandAckP95High`

1. Compare Gateway queue depth, Redis lag, and device reconnect metrics.
2. Separate dispatch latency from device adapter latency.
3. Check whether a single slow device or client is causing pressure.
4. Preserve backpressure limits; do not make queues unbounded to hide latency.

### `FleetRelayTelemetryDropRateHigh`

Telemetry dropping can be intentional coalescing. Check per-device and per-topic rates against declared capabilities. It is an incident only if durable control traffic or critical alerts are affected.

## Troubleshooting

### Console says realtime connection is not ready

Check Gateway A health and its WebSocket upgrade logs. The Nginx console container proxies the exact `/ws` path to `gateway-a:8081`; clients reconnect and obtain current state, but cursor replay is not implemented server-side. `resume.complete` with zero replayed events is only a protocol acknowledgement. A snapshot or status poll must not be interpreted as proof that no intermediate transition was missed. Confirm `WS_ORIGINS` includes the exact console origin.

### Devices never become online

Check both Gateway readiness endpoints, then inspect simulator logs. The prototype uses insecure gRPC and a self-declared device ID; certificate SAN verification is a future mTLS requirement. Presence TTL does not replace a successfully created device session in PostgreSQL.

### Commands remain `PENDING_TRANSPORT`

Check Redis, Dispatcher, outbox age, current presence, the command's attempt count, lease validity, and latest device session. The retry worker scans once per second and uses a fixed three-second attempt deadline. It creates at most `MAX_DELIVERY_ATTEMPTS` attempts (default 3) for the same command; a command that is still `PENDING_TRANSPORT` at that limit receives no more attempts and remains pending until its command expiry changes it to `EXPIRED`. Do not directly update the command row, and do not infer execution from an attempt record: the PostgreSQL insert and Redis publication are separate operations.

### Command reaches `ACK_TIMEOUT`

The worker redelivers a `DISPATCHED` command under the same `command_id` after an attempt deadline, using current Redis presence and a newly validated PostgreSQL attempt. At the configured attempt limit (default 3), or when the command expires after dispatch, it marks the command `ACK_TIMEOUT`. This is a bounded, fixed-interval mechanism: it has no exponential backoff, device execution-state query, or complete late-ACK audit/reconciliation workflow. Redis publication is not atomic with attempt creation, so an attempt row alone is not delivery evidence. Execution remains unknown; do not create a new command or infer non-execution from a missing ACK.

### Development token is rejected

The console's token field uses a static development credential. HTTP requests carry it with Bearer syntax and WebSocket authentication carries the raw value, but it is not a signed or verified production JWT. Check the local `DEV_OPERATOR_TOKEN` configuration and avoid copying it into logs. The Gateway maps known development strings to an operator or viewer identity; the Control API and internal services rely on development identity headers/static shared tokens and plaintext transport. Never expose this stack to an untrusted network or treat these checks as a production security boundary.

### Grafana has no data

Open Prometheus targets at <http://localhost:9092/targets>. Verify service `/metrics` endpoints and collector ports. Current service metrics are minimal placeholders and the Go processes are not OTLP-instrumented, so the supplied dashboards do not yet constitute end-to-end observability evidence.

## Backup, reset, and shutdown

This local topology persists PostgreSQL, Redis AOF, Prometheus, and Grafana in named Docker volumes. Stop without data loss:

```bash
docker compose -f deploy/compose/docker-compose.yml down
```

Before an intentional local reset, record any required evidence. The following command destroys all Compose-managed local state and is not recoverable unless separately backed up:

```bash
docker compose -f deploy/compose/docker-compose.yml down --volumes
```

Never use a reset as an incident recovery procedure. Correct recovery must start from durable PostgreSQL state and the outbox.

## Evidence capture

Every benchmark or failure report should include:

- Git SHA and whether the run used simulator or real hardware.
- CPU, memory, operating system, Go version, Docker version, and Compose version.
- Device, operator, subscription, telemetry, command-rate, duration, and adapter-latency parameters.
- p50, p95, p99, maximum, errors, reconnect distribution, and duplicate side-effect count.
- Raw output and failed acceptance thresholds; never rewrite a design target as a measured result.
