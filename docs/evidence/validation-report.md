> Latest local verification: [2026-09-07 outbox retry isolation](2026-09-07/report.md). Earlier dated sections below retain their historical scope.

# FleetRelay prototype validation report

Validated on 2026-09-01 by separate implementation, review, and test agents. This report records feasibility evidence for the local vertical slice. It is not a production certification and does not upgrade the repository to “V1 complete.”

## Static and build gates

The following checks passed against the delivered tree:

```text
gofmt
go test -race -count=1 ./...
go vet ./...
go build ./cmd/...
buf format --diff --exit-code
buf lint
buf build
npm ci --no-audit --no-fund
npm run lint
npm test
npm run build
docker compose ... config --quiet
```

The full Compose topology also built and started successfully, including PostgreSQL, Redis, two Gateways, three simulators, the console, OpenTelemetry Collector, Prometheus, and Grafana. Service readiness endpoints and the static console were reachable.

## Database evidence

- The initial migration created ten expected tables and the critical lease, sequence, attempt, event, audit, and outbox constraints.
- `up -> down -> up` completed successfully in an isolated PostgreSQL database; the down migration returned the schema to zero application tables.
- Duplicate command submission with the same idempotency key returned the same command ID and produced one command and one attempt.

## Real command paths

The REST path reached a real simulator through the deployed components:

```text
Control API -> PostgreSQL outbox -> Dispatcher -> Redis Stream
-> Gateway -> bidirectional gRPC -> simulator ACK -> PostgreSQL SUCCEEDED
```

The browser contract was exercised through a real WebSocket connection:

```text
authenticate -> snapshot -> lease.acquire(ttl_seconds=30)
-> command.status(PENDING_TRANSPORT) -> command.status(SUCCEEDED)
```

The development authorization matrix rejected unauthenticated reads, legacy spoofable role headers, viewer mutations, and unauthenticated internal callbacks. Operator/viewer reads and authenticated internal calls reached their intended handlers. These are static local-development tokens, not JWT or mTLS evidence.

## Forced Gateway failure

An in-flight command was routed to Gateway A/session generation 7, then Gateway A was terminated with `SIGKILL`. The simulator reconnected to Gateway B/session generation 8. The deadline worker issued later attempts against B; the command moved through `DISPATCHED` and `ACK_TIMEOUT`, and a valid late ACK reconciled it to `SUCCEEDED`.

Observed invariants:

- one PostgreSQL command ID was retained across attempts;
- all associated `command.created` and `command.status` outbox rows were published;
- duplicate delivery used the simulator's same-command cache path;
- the latest Gateway/session/attempt fields were checked on dispatch and ACK.

The simulator has no external physical-execution counter and its idempotency cache is in-process only. Therefore this test demonstrates the control-plane recovery path, but cannot prove exactly-once execution on a restarted process or real device.

Additional final-image recovery checks passed:

- after a Gateway was paused beyond its liveness TTL, the device was reported `OFFLINE`; unpausing rebuilt the liveness key with the same instance identity;
- after a same-ID Gateway restart, an injected stale presence record carrying the old instance identity remained `OFFLINE` even while the new instance was alive;
- the Dispatcher left a command at `PENDING_TRANSPORT` with zero attempts while only that stale presence existed, proving the runtime route gate did not consume the retry budget;
- a second Dispatcher process was rejected by the PostgreSQL session advisory lock, and the active process continuously monitors that lock connection.

## Review disposition

Independent review originally found blocking contract, ACK-validation, consumer recovery, authorization, session replacement, goroutine-lifetime, outbox poison-row, lock-order, stale-Gateway, pending-entry, and multi-Dispatcher issues. Those concrete prototype blockers were fixed and rechecked. The architecture intentionally remains a prototype because the following V1/production work is open:

- durable browser event projection, cursor replay, and scalable live fan-out;
- verified external identity, service credentials, TLS, and device mTLS;
- PostgreSQL/Redis clustered-failover and network-partition testing;
- persistent device idempotency and real-hardware execution evidence;
- measured load/SLO evidence and complete OTLP application instrumentation;
- a production-grade retry scheduler with atomic work claims and richer device execution reconciliation.

The supported claim for this repository is: **a runnable and independently exercised FleetRelay distributed control-plane prototype that demonstrates the proposed command lifecycle and a bounded failover path on a local Compose topology.**

## 2026-09-06 follow-up

The original report above remains historical evidence. Atomic attempt/delivery intents, stable-ID Redis republication, real dependency integration tests, and a new simulator smoke path were subsequently implemented and executed. See the [current report](2026-09-06/report.md) for raw logs and limits. Production claims/permits, durable browser replay, security, device journals and measured load targets remain open.

## 2026-09-07 Docker follow-up

The [new report](2026-09-07-docker/report.md) supersedes current-state statements
about the simulator being in-process-only and the absence of measured latency
or live monitoring evidence. The simulator now supports an optional durable
result journal; the lab executes real container failures, logical backup restore,
Prometheus scrapes/down-alert evaluation and paired baseline/candidate latency
runs. Historical results above retain their original scope. Physical actuator
idempotency, real cloud/production deployment, broad load targets and complete
production gates remain unverified.
