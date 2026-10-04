# FleetRelay Local Operations Runbook

This runbook covers the partial Docker Compose prototype. It documents local measurements and fault exercises; it does not describe a production deployment or certify reliability, security, capacity, or complete observability. Failure behavior below is explicitly labeled either current behavior or a design acceptance target.

Current limitations: durable browser replay, production-grade command scheduling and UNKNOWN-outcome reconciliation, JWT verification, production authorization, service authentication, TLS/device mTLS, and Go OpenTelemetry instrumentation are not implemented. WebSocket reconnect, a fresh snapshot, live status updates, and any current-state polling are recovery aids, not an ordered or lossless event replay. Dispatcher outbox gauges, Prometheus scrapes/down alerts and Grafana provisioning have local execution evidence. Remaining dashboard series and the OTLP collector path are scaffolding.

## Service topology and health

| Service | Internal ports | Healthy when | Dependency failure behavior |
|---|---:|---|---|
| `control-api` | HTTP `8080` | `/healthz` reports liveness; `/readyz` checks PostgreSQL and configured Redis | PostgreSQL-backed mutations return errors when the database is unavailable |
| `gateway-a`, `gateway-b` | HTTP/WS `8081`, device gRPC `9090` | readiness reaches Redis and the Control API readiness endpoint | Connected streams end; the simulator rotates through its configured Gateway addresses and obtains a new session generation |
| `dispatcher` | configured metrics `8082` | `/healthz` reports liveness; `/readyz` checks PostgreSQL and Redis | Commands already committed with outbox rows remain in PostgreSQL |
| `device-sim` | none | configured device sessions are visible | rotates configured Gateways with bounded exponential reconnect delay; optional journal retains simulator results across restart |
| `postgres` | `5432` | `pg_isready` succeeds | no authoritative mutations are accepted |
| `redis` | `6379` | `PING` returns `PONG` | realtime transport pauses; durable state is unchanged |
| `otel-collector` | OTLP `4317`, `4318` | collector configuration loads and its endpoints listen | Go services do not currently emit OTLP application telemetry |
| `prometheus` | host `9092` | targets page reports configured scrapes | most application-specific metrics are not instrumented yet |
| `grafana` | host `3001` | UI loads provisioned dashboards | application correctness is unaffected |

Liveness means the process can answer. Production readiness must prove that the service can safely perform its authoritative role. The local Control API readiness check returns 503 on PostgreSQL failure; it does not certify managed failover or production routing safety.

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

The Docker lab verifies accepted-command recovery through a replacement Gateway. The broader design acceptance target remains:

- Connections on `gateway-a` close and affected devices briefly report offline or reconnecting.
- Devices reconnect through `gateway-b`; new sessions have a higher `session_generation`.
- A late send from Gateway A is rejected as a stale session.
- Durable browser events would resume using the last cursor; the current Gateway does not implement this replay path.
- 95% reconnect within 15 seconds is a target that must be measured, not assumed.

### Redis degradation

```bash
sh scripts/demo-failures.sh redis
```

The Docker lab verifies a queued command remains pending through a Redis pause and succeeds after recovery. The broader design acceptance target remains:

- Already accepted commands remain in PostgreSQL and may remain `PENDING_TRANSPORT`.
- New `REQUIRE_ONLINE` commands return `UNAVAILABLE` because presence cannot be trusted.
- No command is reported as dispatched merely because an outbox row exists.
- After Redis recovers, the Dispatcher republishes stable event and command identities; consumers deduplicate them.

### PostgreSQL fail-closed behavior

```bash
sh scripts/demo-failures.sh postgres
```

The Docker lab verifies readiness failure and retained-command recovery after a PostgreSQL SIGKILL. The broader design acceptance target remains:

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

Check Redis, Dispatcher, outbox age, current presence, the command's attempt count, lease validity, and latest device session. The retry worker scans once per second and uses a fixed three-second attempt deadline. It creates at most `MAX_DELIVERY_ATTEMPTS` attempts (default 3) for the same command; a command that is still `PENDING_TRANSPORT` at that limit receives no more attempts and remains pending until its command expiry changes it to `EXPIRED`. Do not directly update the command row, and do not infer execution from an attempt record: the attempt and delivery intent commit together, while Redis publication is a separate operation.

### Command reaches `ACK_TIMEOUT`

The worker redelivers a `DISPATCHED` command under the same `command_id` after an attempt deadline, using current Redis presence and a newly validated PostgreSQL attempt. At the configured attempt limit (default 3), or when the command expires after dispatch, it marks the command `ACK_TIMEOUT`. This is a bounded, fixed-interval mechanism: it has no exponential backoff, device execution-state query, or complete late-ACK audit/reconciliation workflow. Attempt and delivery intent creation are atomic in PostgreSQL. Redis publication is separate, so an attempt row alone is not delivery evidence. Execution remains unknown; do not create a new command or infer non-execution from a missing ACK.

### Development token is rejected

The console's token field uses a static development credential. HTTP requests carry it with Bearer syntax and WebSocket authentication carries the raw value, but it is not a signed or verified production JWT. Check the local `DEV_OPERATOR_TOKEN` configuration and avoid copying it into logs. The Gateway maps known development strings to an operator or viewer identity; the Control API and internal services rely on development identity headers/static shared tokens and plaintext transport. Never expose this stack to an untrusted network or treat these checks as a production security boundary.

### Grafana has no data

Open Prometheus targets at <http://localhost:9092/targets>. Verify service `/metrics` endpoints and collector ports. Dispatcher outbox gauges are real PostgreSQL observations; the local lab verifies four application scrape targets and the provisioned outbox panel queries through the Grafana API. ACK-latency and other uninstrumented panels can still be empty. Go processes do not emit OTLP traces.

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

## Delivery-intent regression checks (2026-09-06)

`make test-integration` starts dedicated PostgreSQL/Redis containers on loopback
ports 15439/16389, builds race-enabled service binaries, runs the transaction and
publication tests plus a four-process simulator smoke test, and removes only
those disposable test containers/data and temporary binaries. It requires Go
1.26.8+ and Docker Compose. Normal `go test` skips live tests when their explicit
environment variables are absent; CI sets `FLEETRELAY_REQUIRE_INTEGRATION=1` so a
missing dependency setting fails rather than silently skips.

To reproduce the full evidence set (also requires Buf and installed web dependencies):

```sh
docker compose -f tests/integration/compose.yml up -d --wait
export FLEETRELAY_TEST_DATABASE_URL='postgres://fleetrelay:integration-only@127.0.0.1:15439/fleetrelay_test?sslmode=disable'
export FLEETRELAY_TEST_REDIS_ADDR='127.0.0.1:16389'
python3 scripts/verify-delivery.py
docker compose -f tests/integration/compose.yml down --volumes
```

The evidence collector replaces files under `docs/evidence/2026-09-06/`; archive
them first if retaining a previous run. Its baseline negative control checks out
`84206e1` into a temporary directory; it never resets the working repository.

Inspect unresolved publication intents:

```sql
SELECT outbox_id AS delivery_id, aggregate_id AS command_id,
       payload->'attempt'->>'attempt' AS attempt, created_at
FROM outbox WHERE kind='command.delivery' AND published_at IS NULL
ORDER BY created_at;
```

A stable delivery ID can appear under multiple Redis Stream IDs after an ambiguous
XADD. Do not create an extra device attempt manually to clear that backlog.
The worker preflights stale/expired intents, retires them, and retries publication
of current intents. `published_at` is also used for retired intents, so it is not
an audit proof of device receipt. See [ADR-017](adr/017-prototype-delivery-intents.md)
for rollout/rollback compatibility and the remaining scheduler/permit limits.

## Outbox retry isolation (2026-09-07)

Upgrade existing volumes before starting the September 7 store/Dispatcher code:

```sh
docker compose --env-file deploy/compose/.env -f deploy/compose/docker-compose.yml run --rm migrate
```

The guarded `migrations/apply.sql` handles a fresh database and a prior migration-1
volume; rerunning it retains pending retry times. It runs in one transaction with
a migration advisory lock and a five-second lock acquisition limit. A lock timeout
fails the migration rather than starting services with a partial schema. This
procedure is for the local prototype, not a zero-downtime large-table migration.

The Dispatcher still permits one active process. After an XADD or completion
failure, the original row stays pending and receives a database-timed backoff.
Inspect without changing state:

```sql
SELECT outbox_id, kind, aggregate_id, publication_failures,
       created_at, next_publish_at, published_at
FROM outbox
WHERE published_at IS NULL
ORDER BY next_publish_at, created_at, outbox_id
LIMIT 100;
```

`publication_failures` counts transport/completion failures, not device attempts.
For a bad payload, investigate its writer and use logs with the outbox ID; do not
mark it published or delete it merely to silence an alert. For Redis outages,
restore Redis and let the existing UUID/payload retry. A commit-confirmation error
can mean Redis already accepted the message. Duplicate stream entries are allowed.

```sh
curl --fail http://localhost:8082/metrics
make test-integration
# Alert behavior, using the existing Prometheus version:
docker run --rm --network none --entrypoint /bin/promtool \
  -v "$PWD/deploy/observability:/rules:ro" prom/prometheus:v3.6.0 \
  test rules /rules/outbox-alerts.test.yml
```

The three outbox gauges are pending count, delayed-retry count and oldest pending
age. They include delayed rows; an empty due-work batch does not imply a drained
backlog. Metrics query errors return 503, making the Prometheus scrape fail. The
backlog and retry-waiting alerts are validated with promtool. Other dashboard
series such as ACK latency remain planned. `make test-integration` now also needs
`psql`, runs reversible migration checks and uses explicitly disposable services.

For rollback to September 6 code, stop the current Dispatcher and keep migration
2's columns. If reverting the schema, first stop all new-version Go services,
then apply `000002_outbox_retry.down.sql` transactionally; it preserves rows but
loses retry schedules/counts. Earlier rollback must also follow ADR-017. The tests
verify SQL rollback, not a live mixed-version release campaign. See
[ADR-018](adr/018-outbox-retry-isolation.md) and the
[verification report](evidence/2026-09-07/report.md).


## Isolated Docker recovery and latency lab

Use `tests/docker/compose.yml` for the measured follow-up, separate from the
operator-demo topology. It binds service ports to dynamic loopback ports and uses
only the `fleetrelay-evidence-lab` project. Application containers run as non-root
with a read-only root, dropped capabilities, no-new-privileges, a writable `/tmp`,
1 CPU and 256 MiB limits. PostgreSQL/Redis and per-device journals use named volumes.
The lab intentionally uses static local credentials and plaintext networking.

An exact pre-optimization working-tree archive is retained with the report's raw
artifacts; the older Git HEAD alone does not include the September 7 baseline.
Extract that archive to `/tmp/fleetrelay-baseline` and supply its `baseline`
subdirectory. Docker Engine/Compose v2 and Python 3 are required:

```sh
python3 scripts/build-docker-lab.py --baseline /tmp/fleetrelay-baseline/baseline
python3 scripts/verify-docker-lab.py --phase all --output /tmp/fleetrelay-docker-lab
# Inspect only this disposable project:
docker compose -p fleetrelay-evidence-lab -f tests/docker/compose.yml ps
```

The harness submits commands through REST and observes terminal results through
REST polling. For this fixture only it extends acquired lease deadlines to one
hour using SQL; command TTL is two minutes. Normal application lease defaults
remain unchanged. Reliability runs use real container SIGKILL/pause/restart,
real PostgreSQL/Redis and a synthetic device effect. The logical restore compares
row counts and ordered row-JSON hashes across seven core tables in another
local database. It is not PITR or a restore in another region.

Performance runs alternate AB/BA order across five pairs, recreate only the
Dispatcher, exclude four warm-up commands per device, and measure 25 commands
per device per run on four concurrent sequential clients. Baseline and candidate
use the same other services, simulator journals and resource limits. The report
compares the median of five per-run nearest-rank p95 values. This closed-loop
experiment is not a saturation test or production SLO assessment.

Archive output before another run (the harness overwrites same-named result
files). To finish or recover an interrupted experiment, inspect this project's
paused/stopped services and resume/recreate them. Once its evidence is archived,
the following removes **only the disposable lab state**, including journals:

```sh
docker compose -p fleetrelay-evidence-lab -f tests/docker/compose.yml --profile fault down --volumes
```

The [executed report](evidence/2026-09-07-docker/report.md) separates container,
simulator, hardware, cloud and production evidence. No hosting, cloud account,
physical device or production traffic is involved.
