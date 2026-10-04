# Delivery-intent verification — 2026-09-06

**Result:** 15 top-level Go tests passed with the race detector; 0 failed and 0
skipped. The real-service simulator path reached `SUCCEEDED` with one command,
one attempt and one delivery intent after duplicate REST submission. A negative
control on the old commit failed at the intended atomicity oracle.

## Revision and environment

Base commit: `84206e198423529c244ef4ac12072230a004e8df`. The changes are a local,
uncommitted working tree; that SHA is **not** a permalink to the new code.
[summary.json](summary.json) records source hashes, exact commands, exit codes,
elapsed times and environment. A final source manifest refresh includes the
documentation completed after the tests; compiled Go inputs were not changed.

Linux 7.0.0-29 x86_64; Intel i9-13900K; 32 logical CPUs; reported memory
129,209,084 KiB. Go 1.26.8, Docker 29.8.0, PostgreSQL 17.6, Redis 8.2 image.
Native Go test/service processes connect to dedicated local containers on
loopback ports 15439 and 16389. Tests create random disposable database schemas.
The E2E test starts control-api, stream-gateway, dispatcher and device-sim from
race-enabled native builds. No physical robot, cloud, GPU or production traffic.

Local frontend checks used Node 20.19.5 and installed lockfile-era dependencies;
GitHub CI is configured for Node 22. This run did not execute `npm ci` or prove
Node 22/hosted-CI parity. Buf 1.72.0 was available. Background workstation
containers were left running; this is not a controlled performance environment.

## Executed checks

| Check | Result / raw evidence |
|---|---|
| `go test -race -count=1 -json ./...` | 15 top-level pass, 0 fail, 0 skip; [JSON event log](go-race.log). Subtest events are excluded from the 15 count. |
| Real PostgreSQL + Redis tests | Commit rollback, concurrency, storage reopen, ACK deadline, obsolete intents and lost XADD reply passed in the same Go run. |
| Four real Go services + deterministic simulator | REST accept and duplicate → PostgreSQL intent → Redis → Gateway → bidirectional gRPC → simulator ACK → PostgreSQL `SUCCEEDED`; 1 attempt / 1 intent. |
| `go vet`, normal build and race build | Passed; [vet](go-vet.log), [build](go-build.log), [race build](race-build.log). |
| Formatting and Protobuf contract | Passed; [gofmt](gofmt.log), [Buf format](buf-format.log), [lint](buf-lint.log), [build](buf-build.log). Protobuf content was unchanged. |
| Frontend lint, Vitest, production build | Passed; [lint](web-lint.log), [1 test](web-test.log), [build](web-build.log). |
| Original and test Compose parsing | Passed; [original](compose.log), [test](integration-compose.log). Test services actually started and were healthy. |
| Old-revision negative control | Expected exit 1, `attempt committed without its delivery intent`; [log](baseline-regression.log), [exact regression source](baseline-regression.go.txt). |

The collector has 14 check records: 13 expected successes and one **expected
failure** against the baseline. This is not 14 passing tests. The baseline
negative control is excluded from the current 15/0/0 test result.

## Failure oracles and scope

- **Commit atomicity:** a PostgreSQL trigger rejects the delivery-intent insert
  after the attempt insert. The current transaction leaves 0 attempts, 0 delivery
  intents and 1 unhandled created row. Removing the trigger permits recovery. The
  same injected boundary on `84206e1` exposes its lack of a delivery insert.
- **Contending creators:** 16 goroutines call the real PostgreSQL adapter for one
  command; exactly 1 creation succeeds and the database has 1 attempt / 1 intent.
  This tests a narrow database contention invariant, not 16 distributed nodes.
- **Publication not confirmed:** five injected failures before XADD leave no
  Stream message and keep the same pending intent. No additional device attempt
  is admitted even after the original deadline.
- **Ambiguous XADD:** a hook invokes real XADD successfully, then reports failure.
  Recovery yields 2 Redis messages with the same delivery ID and attempt 1. The
  database still has one attempt / intent. This demonstrates at-least-once
  publication, not deduplication of Redis entries or physical device effects.
- **Storage recovery:** close/reopen PostgreSQL adapters; read unchanged stored
  delivery ID/payload. This models loss of process-local state after commit; it
  is not an OS-level process crash or PostgreSQL failover test.
- **ACK timing:** a publication confirmed 20 seconds after scheduling provides a
  fresh three-second ACK window. Duplicate confirmation cannot extend it.
- **Obsolete work:** replacement session, released lease, command expiry and
  terminal ACK are rejected by publication preflight. Expiry remains visible to
  the resolver while publication is pending.

## Reproduction and remaining limits

Run `make test-integration` for disposable live integration/E2E verification.
For the full evidence collector, follow the commands in the
[runbook](../../runbook.md#delivery-intent-regression-checks-2026-09-06).
The collector reconstructs a temporary old tree via `git archive 84206e1` and
copies the exact standalone regression oracle into it.

No throughput, latency, percentile or availability benchmark was run. Elapsed
test durations are diagnostic metadata, not performance improvements. No fresh
application Docker image build, hosted CI run, Gateway SIGKILL, mixed-version
rollout/rollback, multi-node failover or Kubernetes deployment was executed.
The 2026-09-01 forced-Gateway test remains historical evidence only.

Single active Dispatcher, coarse polling, missing durable work claims/dispatch
permits, possible poison-row batch starvation, in-process simulator deduplication,
static development identities, plaintext transport, missing durable browser
replay and OTLP instrumentation remain material limitations. See
[ADR-017](../../adr/017-prototype-delivery-intents.md) for compatibility and costs.
