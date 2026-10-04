# Outbox retry isolation verification — 2026-09-07

**Result:** the final full Go run passed **23 top-level tests**, with **0 failures
and 0 skips**, under the race detector. A healthy outbox event was published to
real Redis despite 64 older malformed delivery rows. PostgreSQL migration and
Prometheus rule checks passed. No performance improvement is claimed.

## Snapshot and environment

Local base HEAD: `84206e198423529c244ef4ac12072230a004e8df`, branch `main`. September
6 work was already uncommitted at task start. September 7 extends that starting
working tree; the old SHA is not a GitHub permalink to either local update.
[summary.json](summary.json) contains checked source hashes, test names, commands,
exit codes, environment, baseline hashes and the persistent raw archive location.

Linux 7.0.0-29 x86_64, Intel i9-13900K, 32 logical CPUs, reported memory 129,209,084
KiB; Go 1.26.8, Docker 29.8.0. PostgreSQL **17.6**, Redis executable **8.2.9** from
the existing `redis:8.2-alpine` image tag. The test project is
`fleetrelay-review-20260907`, with loopback ports 15439/16389 and disposable data.
The E2E path uses four race-built native Go processes and a deterministic device
simulator. Other workstation workloads were not controlled. No real hardware,
cloud, GPU, user traffic or production database was used.

## Executed checks and oracles

| Check | Observed result |
|---|---|
| Full `go test -race -count=1 -coverprofile=… -covermode=atomic -json ./...` | Final 23/0/0 top-level pass/fail/skip. Subtests and packages with no tests are not extra passing cases. The earlier 22/0/0 run preceded the final completion-failure regression. |
| Poison-row progress | Inject 64 JSONB envelopes of the wrong Go type followed by one healthy event. Within three batch calls, its database marker is handled and Redis contains its event ID; all 64 malformed rows remain pending. This is a finite fault fixture, not throughput or a fairness SLA. |
| Identical starting-tree negative control | Expected exit 1: `healthy outbox row starved behind 64 poison rows after 3 batches`. Baseline uses the captured starting worktree plus a test-only mechanical extraction of the original main-loop batch into a callable helper. The same formatted regression body is used on both sides. |
| Persisted retry / stale results | Reopen the PostgreSQL adapter; delayed work remains excluded until due, identity/payload/count survive, a stale failure cannot move its deadline, handled work cannot revive, and publication retry leaves one device attempt. |
| Redis accepted, confirmation failed | A PostgreSQL trigger rejects the completion update after real XADD. The same intent is deferred; retry produces two Redis entries with one delivery ID/attempt, then commits completion. |
| Per-item timeout | A Redis hook blocks the first XADD until its context expires; that row is deferred and the next row succeeds in the same batch. The hook models a stalled dependency; it is not a physical network partition. |
| Backlog collection | Ready and delayed rows both count; oldest age reflects database time. Draining yields zero gauges. A closed database adapter makes the actual HTTP router return 503 without false zero series. |
| Migration SQL | Up/down preserve rows and completion markers. The actual psql bootstrap runs on a fresh schema, reruns without resetting retry state, and upgrades existing rows after rollback. The fixture transaction rolls back completely. |
| Four-process simulator E2E | Duplicate REST submission retains one command; PostgreSQL → Redis → Gateway → gRPC simulator → ACK reaches SUCCEEDED with one attempt/intent. Real Dispatcher HTTP metrics eventually report a drained outbox. |
| Existing delivery regressions | Atomic rollback, 16 concurrent attempt creators, before/after-XADD response failures, ACK timing and lease/session/expiry/terminal fences still pass. |
| Go vet, normal/race builds, gofmt, Buf format/lint/build | Passed. Final test additions also passed targeted vet and formatting. |
| Compose parsing, alert rules, dashboard JSON | Compose passed. Real promtool v3.6.0 passed four alert expectations: backlog/retry firing and zero-backlog non-firing. Updated dashboard JSON parses; live Grafana rendering was not exercised. |
| Frontend | Existing installed dependencies: 1 Vitest test, lint and production build passed; Node 20.19.5 locally, versus Node 22 configured in CI. No new `npm ci` or hosted CI run. |

The fixture initially had an ambiguous UUID/text SQL parameter and was corrected
before the recorded negative-control run. That setup error is not the claimed
starvation reproduction. Raw exploratory and final logs are kept separately.

## Coverage and reproducibility

Application statement coverage is **22.0%** after filtering only generated
`/gen/` and third-party `/web/node_modules/` entries from the final Go profile.
The four native service subprocesses were race-built without `-cover`; their
executed code is not credited to this profile. The number exposes a remaining
coverage gap and is not used as a resume achievement or improvement claim.

For the maintained integration path, install local `psql`, Go and Docker, then:

```sh
make test-integration
```

For the full Go run, start the dedicated Compose dependencies and export the
explicit test DSN, Redis address and a temporary directory containing
`go build -race -o "$FLEETRELAY_TEST_BIN_DIR/" ./cmd/...`, as in
`scripts/test-integration.sh`; run the full command recorded in `summary.json`.
The bank-sync verification archive preserves raw logs, coverage profiles,
starting-source manifest/archive, baseline helper and the temporary evidence
collector. Temporary logs are not added as repository source files.

## Unverified boundaries

Single active Dispatcher and coarse polling remain. No durable distributed claim,
strict tenant fairness, DLQ, malformed-payload repair workflow or deadline-scan
redesign is implied. The queue metrics are direct Prometheus text exposition;
OTLP traces and other application metrics remain planned. No fresh application
Docker image build, live Prometheus scrape/evaluation loop, live Grafana session,
mixed-version rollout, database/Redis failover, physical network fault, process
SIGKILL campaign or hosted CI run was performed. SQL rollback is not a live
release rollback. Persistent device journaling, real-hardware idempotency,
production identity/TLS, durable browser replay and SLO/load validation remain
open. No throughput, latency, availability, users or cloud deployment metric is
resume-eligible.
