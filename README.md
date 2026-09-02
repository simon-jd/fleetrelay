# FleetRelay

FleetRelay is a prototype Go control plane for real-time device fleets. It uses bidirectional gRPC for devices, WebSocket for operators, PostgreSQL as the authoritative control store, and Redis Streams for cross-instance transport. The included reference application models three vacuum robots across three floors.

The design target is deliberately narrow: accepted commands are durable, delivery is at least once, and device execution is idempotent. The current implementation is a tested vertical-slice prototype: PostgreSQL command/outbox transactions, bounded deadline-driven redelivery, late-ACK reconciliation, cross-Gateway device failover, and simulator-side command caching exist. Durable browser replay, production identity, and real-hardware execution evidence are not complete. It therefore makes no production-readiness or exactly-once claim.

## Implementation status

| Area | Current status |
|---|---|
| Command persistence and fencing | Prototype/verified locally: database transactions, state-machine and attempt checks, bounded same-command redelivery, late ACKs, single-active Dispatcher, and a cross-Gateway SIGKILL scenario have passed. This is not a distributed proof or production HA certification. |
| Browser recovery | Reconnect obtains a current snapshot and the console may refresh current status. Live WebSocket updates, point-in-time HTTP reads, polling, cursor storage, and the current zero-event `resume.complete` response are not durable replay; events during a disconnect can be missed. |
| Security | Development-only static tokens and identity headers over plaintext transports. The console's Bearer-shaped token is not a verified JWT, and production authentication, authorization, service identity, TLS, and device mTLS are pending. |
| OpenTelemetry | Collector, Prometheus, Grafana, and dashboard scaffolding only; Go services are not yet instrumented to emit OTLP traces or application metrics. |
| Reliability evidence | Independent tests passed the real REST and WebSocket command paths, idempotent duplicate submission, reversible migrations, and one forced Gateway failover. Persistent device idempotency, load targets, and real hardware remain unverified. |

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
- Redis does not authoritatively change command status. Gateway delivery uses consumer groups, pending-entry recovery, PostgreSQL attempt validation, and terminal-entry retirement, but Redis publication is still not atomic with the PostgreSQL attempt insert.
- `lease_epoch`, `device_sequence`, attempt, Gateway, and `session_generation` are checked before dispatch and ACK. A forced Gateway SIGKILL and session-generation failover passed locally; broader network partitions and multi-node PostgreSQL/Redis failover have not been certified.
- The Dispatcher scans expired attempt deadlines once per second (up to 64 candidates), re-resolves current Redis presence, revalidates the command, lease, latest session, and device ordering in PostgreSQL, then creates another attempt for the same `command_id`. Attempts use a fixed three-second deadline and stop at `MAX_DELIVERY_ATTEMPTS` (default 3); there is no exponential backoff or per-command execution-state query.
- When the attempt limit or command expiry is reached after `DISPATCHED`, the worker can mark `ACK_TIMEOUT`. A `PENDING_TRANSPORT` command stops receiving attempts at the same limit but remains pending until its command expiry, when it becomes `EXPIRED`. A valid late ACK may reconcile `ACK_TIMEOUT` to its device result. Redis publication and the PostgreSQL attempt insert are not atomic, so this remains bounded prototype redelivery rather than a general durable retry guarantee.
- A slow WebSocket client can be disconnected with code 1013. Reconnect, snapshots, live updates, and any current-status polling do not provide durable cursor replay; transitions during the gap can be missed.

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

Compose is a development environment. The browser sends a static development token in an `Authorization: Bearer ...` header for HTTP reads and in the WebSocket `authenticate` message. This Bearer syntax does not make the value a JWT: the Control API does not authoritatively verify it, while the Gateway only compares it with configured development token strings and then supplies development identity headers. Internal callbacks use another static shared token. All of these flows use plaintext local HTTP/gRPC, and device identity is self-declared without mTLS. These controls are suitable only for an isolated workstation, not an untrusted network or production. A production deployment must add managed secrets, verified external identity and authorization, service authentication, TLS/mTLS certificates, strict origin controls, network policy, encrypted storage, and tested backup/restore.

## Evidence policy

The performance numbers in the design are acceptance targets, not measured results. Benchmark reports must record the Git SHA, machine, OS, Go and container versions, full workload parameters, sample size, and raw output. Simulator and real-hardware results must remain clearly separated.
