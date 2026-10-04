# FleetRelay

## Experience Bank results

The results below are the owner-confirmed results from a separate cloud-hosted test environment, synchronized from the Experience Bank. The experiments retain the local, synthetic, simulator, CPU, Docker and single-host boundaries stated in each result; cloud hosting does not imply production deployment. This repository refresh does not represent a rerun of those measurements. Earlier dated evidence below remains tied to its own source, configuration and denominator.

1. Committed command intent together with its attempt/outbox row in one PostgreSQL 16 transaction and kept the Redis 7 Streams message as a wake-up hint only, so the outbox and the fixed 500 ms fallback scan remain the recovery path and nothing is dispatched before the commit.

2. Quarantined malformed rows and persisted retry eligibility as next_attempt_at, so a query is not stuck behind the first 64 bad records and pending work survives restarts.

3. Cut median per-round p95 of command accept to control-plane ACK from 532 ms to 271 ms (49.1%) across five alternating baseline/candidate pairs on four gRPC device simulators, 100 commands per side per pair and 1,000 commands total; baseline and candidate shared the same reliability logic and differed only in the immediate follow-up over committed intents.

4. Persisted an accept record and exactly one simulated result per command ID in a replayable device log before ACK, and carried lease epoch, device sequence number and session generation on commands so crash/restart and redelivery recover from the log with no second simulated effect.

5. Covered eight recovery scenarios, including crashes before and after ACK, Redis replay, old-session reconnect, bad-row isolation and recovery drills, and checked primary-key sets, state constraints and row contents across seven tables after restore on the local Docker host.

See the [implementation and reproduction map](docs/experience-bank-alignment.md) for per-result source/tests, reproduction commands and limitations.

FleetRelay is a prototype Go control plane for real-time device fleets. It uses bidirectional gRPC for devices, WebSocket for operators, PostgreSQL as the authoritative control store, and Redis Streams for cross-instance transport. The included reference application models three vacuum robots across three floors.

The design target is deliberately narrow: accepted commands are durable, delivery is at least once, and device execution is idempotent. The current implementation is a tested vertical-slice prototype: PostgreSQL command/outbox and atomic attempt/delivery-intent transactions, bounded deadline-driven redelivery, late-ACK reconciliation, cross-Gateway device failover, durable publication backoff, backlog metrics, and optional persistent simulator results exist. Durable browser replay, production identity, and real-hardware execution evidence are not complete. It therefore makes no production-readiness or exactly-once claim.

## Implementation status

| Area | Current status |
|---|---|
| Command persistence and fencing | Prototype/verified locally: database transactions, state-machine and attempt checks, stable-ID publication recovery, bounded same-command redelivery, late ACKs, single-active Dispatcher, and a historical cross-Gateway SIGKILL scenario have passed. This is not a distributed proof or production HA certification. |
| Browser recovery | Reconnect obtains a current snapshot and the console may refresh current status. Live WebSocket updates, point-in-time HTTP reads, polling, cursor storage, and the current zero-event `resume.complete` response are not durable replay; events during a disconnect can be missed. |
| Security | Development-only static tokens and identity headers over plaintext transports. The console's Bearer-shaped token is not a verified JWT, and production authentication, authorization, service identity, TLS, and device mTLS are pending. |
| Observability | Dispatcher exports real committed-state outbox gauges for pending rows, delayed retries and oldest age; backlog alerts and dashboard queries are wired. Other application series and OTLP traces remain unimplemented. |
| Reliability evidence | Real local PostgreSQL/Redis integration tests cover publication ambiguity, concurrent attempt creators, poison-row isolation and migrations. The [Docker lab](docs/evidence/2026-09-07-docker/report.md) additionally verifies application images, Gateway/Redis/PostgreSQL failure recovery, simulator restart replay, live Prometheus alerting and logical backup restore. Physical device idempotency, cloud deployment and production load targets remain unverified. |

## Architecture

```text
React console ── REST/WebSocket ──► stream-gateway (2 instances)
                                           │ gRPC device streams
                                           ▼
                                     device simulator
        │
        ├──► control-api ──► PostgreSQL (state, lease, audit, outbox)
        └──► dispatcher  ──► Redis Streams (routing and live fan-out)

Planned OTLP instrumentation ──► OpenTelemetry Collector ──► Prometheus/Grafana
```

The product thesis and V1 correctness arguments are in [the V1 specification](2026-08-31-fleetrelay-v1-design.md). The target security, tenancy, durable replay, HA/DR, SLO, release, and go-live gates are defined in [the production design baseline](docs/production-design.md). Operational procedures are in [the runbook](docs/runbook.md), and reproducible prototype evidence is summarized in [the validation report](docs/evidence/validation-report.md).

## Local stack

Requirements:

- Docker Engine 27+ with Compose v2
- 4 CPU cores and 6 GB of free memory recommended
- `curl` for smoke checks

Start the prototype stack:

```bash
cp deploy/compose/.env.example deploy/compose/.env
docker compose --env-file deploy/compose/.env -f deploy/compose/docker-compose.yml up --build -d
docker compose -f deploy/compose/docker-compose.yml ps
```

Open the operator console at <http://localhost:3000> and Grafana at <http://localhost:3001>. The development credentials in `.env.example` are intentionally local-only and must not be reused outside an isolated workstation.

| Endpoint | Purpose |
|---|---|
| `localhost:3000` | React operator console |
| `localhost:3001` | Grafana (`admin` / configured password) |
| `localhost:8080` | Control API REST, health and metrics |
| `localhost:8081` | Gateway A WebSocket and health |
| `localhost:8083` | Gateway B WebSocket and health |
| `localhost:9090` | Gateway A device gRPC |
| `localhost:9091` | Gateway B device gRPC |
| `localhost:9092` | Prometheus |

Readiness checks:

```bash
curl --fail http://localhost:8080/readyz
curl --fail http://localhost:8081/readyz
curl --fail http://localhost:8083/readyz
```

Stop the stack without deleting its durable volumes:

```bash
docker compose -f deploy/compose/docker-compose.yml down
```

Use `down --volumes` only when you explicitly want to erase local PostgreSQL, Redis, Prometheus, and Grafana data.

## Development

Run the backend checks:

```bash
go test -race ./...
# With Docker and psql available, run real PostgreSQL/Redis and simulator checks:
make test-integration
go vet ./...
buf format --diff --exit-code
buf lint
buf build
```

Run the console separately with REST and WebSocket proxies to the local Go services:

```bash
cd web
npm install --no-audit --no-fund
npm run lint
npm test
npm run dev
```

The console saves a static development token in session storage and a prospective cursor in local storage. It reconnects with bounded exponential backoff, subscribes for a fresh snapshot, and sends a `resume` request when a cursor exists. The Gateway currently answers that request with `resume.complete` and `replayed: 0`; it does not read or replay a durable event log. A current-state refresh or command-status poll can close some display gaps but cannot prove that every intermediate transition was observed. Telemetry is a latest-value view, not replayable audit history.

## Failure demonstrations

Start from a healthy stack, open the Failure Injection dashboard, then run exactly one scenario:

```bash
sh scripts/demo-failures.sh gateway
sh scripts/demo-failures.sh redis
sh scripts/demo-failures.sh postgres
```

Each run attempts to recover the stopped service and writes a machine-readable infrastructure result under `docs/evidence/failures/`. That file proves only dependency recovery. Command-path results must be recorded separately as described in the validation report; neither artifact proves durable browser replay or a production acceptance threshold.

## Delivery semantics and current limits

- PostgreSQL is the prototype's source of truth for leases, commands, attempts, audit records, and outbox rows.
- Redis does not authoritatively change command status. Gateway delivery uses consumer groups, pending-entry recovery, PostgreSQL attempt validation, and terminal-entry retirement. Each new attempt and its immutable `command.delivery` intent now commit together in PostgreSQL; Redis XADD remains a separate, retryable side effect with a stable `delivery_id`.
- `lease_epoch`, `device_sequence`, attempt, Gateway, and `session_generation` are checked before dispatch and ACK. A forced Gateway SIGKILL and session-generation failover passed locally; broader network partitions and multi-node PostgreSQL/Redis failover have not been certified.
- The Dispatcher scans expired attempt deadlines once per second (up to 64 candidates), re-resolves current Redis presence, revalidates the command, lease, latest session, and device ordering in PostgreSQL, then creates another attempt for the same `command_id`. Attempts use a three-second ACK deadline starting at confirmed publication and stop at `MAX_DELIVERY_ATTEMPTS` (default 3); there is no exponential backoff or per-command execution-state query.
- When the attempt limit or command expiry is reached after `DISPATCHED`, the worker can mark `ACK_TIMEOUT`. A `PENDING_TRANSPORT` command stops receiving attempts at the same limit but remains pending until its command expiry, when it becomes `EXPIRED`. A valid late ACK may reconcile `ACK_TIMEOUT` to its device result. Publication failures use persisted equal-jitter exponential backoff (0.5–1 seconds initially, capped at 15–30 seconds) and two-second per-item deadlines; ready rows are ordered by their next publication time. These transport retries are separate from the device ACK retry policy. An unpublished delivery intent suppresses new attempts so Redis publication failures do not consume additional device retry budget; command expiry still resolves pending state. Lost publication responses can produce duplicate Stream messages with the same delivery ID. Single-Dispatcher ownership, a 64-row polling batch, and the absence of durable claims/permits remain prototype limits.
- A slow WebSocket client can be disconnected with code 1013. Reconnect, snapshots, live updates, and any current-status polling do not provide durable cursor replay; transitions during the gap can be missed.
- After handling a created-command event, at most one additional 64-row pass reads newly committed delivery intents without waiting for another 250 ms tick. This bounded optimization preserves normal validation and retry handling; a backlog can still delay the new intent.
- Set `DEVICE_JOURNAL_PATH` to retain simulator results, command fingerprints and sequence/lease fences across process restart. A simulated effect counter and result share one fsynced snapshot; this does not establish atomicity with physical hardware. The journal stops at 10,000 records and fails closed on invalid data or uncertain writes. Default Compose remains ephemeral unless a journal path and durable volume are configured; the Docker lab configures both.

## Repository map

```text
api/                    versioned Protobuf contracts
cmd/                    Go service entry points
internal/               domain and infrastructure packages
migrations/             PostgreSQL schema
web/                    minimal React/TypeScript operator console
deploy/compose/         reproducible local topology
deploy/observability/   collector and Prometheus/Grafana scaffolding
scripts/                failure demonstrations
docs/                   runbook, design notes and generated evidence
```

The [documentation index](docs/README.md) distinguishes target specifications from currently verified behavior.

## Security boundary

Compose is a development environment. The browser sends a static development token in an `Authorization: Bearer ...` header for HTTP reads and in the WebSocket `authenticate` message. This Bearer syntax does not make the value a JWT: the Control API and Gateway compare it with configured static development tokens; internal identity headers require the shared internal token. These checks implement only the local development identity model. Internal callbacks use another static shared token. All of these flows use plaintext local HTTP/gRPC, and device identity is self-declared without mTLS. These controls are suitable only for an isolated workstation, not an untrusted network or production. A production deployment must add managed secrets, verified external identity and authorization, service authentication, TLS/mTLS certificates, strict origin controls, network policy, encrypted storage, and tested backup/restore.

## Evidence policy

The [2026-09-07 update](docs/market-review-2026-09-07.md), [retry ADR](docs/adr/018-outbox-retry-isolation.md), and [new verification report](docs/evidence/2026-09-07/report.md) extend the current scope. Existing volumes must run `migrations/apply.sql` through the Compose migrate service before starting the updated services; see the [runbook](docs/runbook.md#outbox-retry-isolation-2026-09-07).

The [2026-09-06 update](docs/market-review-2026-09-06.md), [delivery ADR](docs/adr/017-prototype-delivery-intents.md), and [executed evidence](docs/evidence/2026-09-06/report.md) document the current scope. The new Dispatcher is required to handle `command.delivery` rows; follow the ADR before rollback.

The performance numbers in the design are acceptance targets, not measured results. Benchmark reports must record the Git SHA, machine, OS, Go and container versions, full workload parameters, sample size, and raw output. Simulator and real-hardware results must remain clearly separated.

The [Docker lab report](docs/evidence/2026-09-07-docker/report.md) provides a separate
measured baseline/candidate comparison for the bounded follow-up pass, using four
simulated devices and resource-limited containers on one workstation. See
[ADR-019](docs/adr/019-local-docker-recovery-and-bounded-dispatch.md) and the
[reproduction guide](docs/runbook.md#isolated-docker-recovery-and-latency-lab).
