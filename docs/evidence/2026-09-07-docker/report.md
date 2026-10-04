# FleetRelay Docker recovery and measured latency — 2026-09-07

Run ID: `fleetrelay-local-docker-20260907`. Completed: 2026-09-07T23:26:37.914696+00:00.
Scope: **local Docker services and simulated devices**. No physical device,
cloud account, production deployment or production traffic was used.

## Implemented changes

- Optional simulator result journal with exclusive ownership, deterministic
  command fingerprint, sequence/lease fences, fsync/rename, a 10,000-entry bound
  and fail-closed handling of corruption or ambiguous writes. A fault option
  exits after durable result storage but before its ACK.
- Dispatcher performs at most one extra 64-row due-work pass after handling a
  created-command event. It reads committed intents and retains all publication
  validation/backoff/confirmation handling; there is no uncommitted fast send.
- Maintained Docker topology, image builder and reliability/performance harness.
  All application services run as non-root with read-only roots, no capabilities,
  no-new-privileges, PID/memory/CPU limits and explicit writable volumes.
- Current documentation corrects outdated health, failover, in-memory cache,
  image-build and monitoring descriptions. The production matrix remains Proposed.

These changes extend the backend reliability, fault handling, observability and
measurement gaps identified in the [official-JD and ecosystem review](../../market-review-2026-09-07.md).
No dependency upgrade, new framework or cloud resource was needed. See
[ADR-019](../../adr/019-local-docker-recovery-and-bounded-dispatch.md).

## Executed recovery observations

| Scenario | Actual oracle/result | Evidence boundary |
|---|---|---|
| Container deployment and REST idempotency | Five inspected app instances have non-root UID, read-only root, 1 CPU and 256 MiB limits; duplicate POST gives one command and one attempt | Local images/runtime, not security certification |
| Simulator crash after persist/before ACK | Exit injection, one process restart, two attempts, one increment in durable simulated effect count, same command succeeds | Counter and cached result are in one snapshot; no physical actuator atomicity |
| Gateway SIGKILL | Exit 137; session moves A → B; accepted command succeeds after 1.40 s | Dispatcher paused before scheduling; not an in-flight hardware execution test |
| Redis pause | Publication failure persists; command stays PENDING_TRANSPORT and succeeds 0.61 s after resume | Single local Redis resumes its retained state; not empty-cluster rebuild |
| PostgreSQL SIGKILL | API readiness returns 503; retained committed command succeeds after 3.54 s | WAL recovery on one retained local volume, not managed failover/RPO certification |
| Live monitoring | Four real application scrape targets up; outbox samples present; Grafana dashboard API contains provisioned queries | No visual panel assertion, no complete application tracing |
| Logical backup/restore | pg_dump/pg_restore to another DB; counts and ordered row-JSON hashes match across seven core tables | Same PostgreSQL instance, small fixture; not PITR/independent-region restore |
| Live down alert | Actual FleetRelayTargetDown Dispatcher alert fires after configured hold and resolves after restart | No external alert notification sent |

All **8 scenarios passed**. Recovery seconds above are single observations with
different triggers and small fixtures; they are not RTO percentiles or SLAs.
The backup contained five commands and six attempts. Runtime and SQL evidence is
embedded in [summary.json](summary.json); original journals/logs/dump are archived.

## Paired latency measurement

A = archived pre-follow-up Dispatcher. B = bounded follow-up Dispatcher.
Each uses the same other candidate services, persistent simulator journals and
resource limits. The only application logic changed in the Dispatcher is its
bounded follow-up pass. The candidate Dockerfile also creates a journal directory;
it is unused by the Dispatcher. Image IDs and source hashes are recorded.

- One Linux workstation: 13th Gen Intel(R) Core(TM) i9-13900K, 32 logical CPUs,
  MemTotal:       129209084 kB; Linux-7.0.0-29-generic-x86_64-with-glibc2.43.
- go version go1.26.8 linux/amd64; Docker 29.8.0, Compose 5.3.1; PostgreSQL
  17.6, Redis 8.2.9, Prometheus 3.6.0, Grafana 12.1.1.
- Four concurrent clients, each sequentially submits 25 commands per measured run;
  100 commands per run, five runs per variant, **500 + 500 measured commands**.
- Four warm-up commands per device before each run are excluded (160 total).
  Synthetic adapter delay 25 ms, REST status poll 20 ms, command TTL two minutes.
  The fixture extends acquired lease expiry to one hour using SQL before load;
  command creation/result observation use REST. Default application lease is unchanged.
- Latency starts just before command POST and ends when REST observes SUCCEEDED;
  it includes network, server, simulator fsync and polling delay. Each run p95 is
  nearest-rank over 100 raw samples. The headline is the median of five run p95s,
  not a pooled percentile, server-only histogram or maximum sustainable capacity.

| Pair | Order | Baseline p95 ms | Candidate p95 ms | Baseline commands/s | Candidate commands/s |
|---|---|---:|---:|---:|---:|
| 1 | A → B | 518.20 | 282.23 | 8.06 | 16.15 |
| 2 | B → A | 519.02 | 278.16 | 8.03 | 16.12 |
| 3 | A → B | 542.03 | 281.70 | 8.02 | 16.02 |
| 4 | B → A | 531.56 | 262.30 | 8.02 | 16.19 |
| 5 | A → B | 538.33 | 269.06 | 8.03 | 16.05 |

Median run p95: **531.56 →
278.16 ms**, a **47.67% reduction**.
Formula: `(baseline median run p95 − candidate median run p95) / baseline × 100`.
All 1,000 measured commands succeeded; all command IDs are unique. The saved raw
samples were independently re-parsed and per-run p95/aggregate values recomputed.

The host has no exclusive CPU isolation; the shared local database and simulator
journals grow between runs. Alternating order reduces one-sided warm-up/growth
bias but is not a statistical confidence interval. No open-loop saturation,
N-1 failure-under-load, burst, many-device scale or long soak was measured.
Throughput is closed-loop achieved rate, not certified capacity. The result
supports this local fixture only and cannot be presented as a production gain.

## Regression and failed checks

- **29 top-level Go tests passed under `-race`, zero failed or skipped** with real
  dedicated PostgreSQL/Redis and race-built native service subprocesses.
- New journal tests cover reopen/replay, changed fingerprint, corruption,
  wrong-device/second-owner refusal, ambiguous persistence, 16 concurrent copies,
  cancellation and fences. A new real-dependency test verifies one batch publishes
  the same committed delivery ID; its prior-code negative control fails as expected.
- Go vet, gofmt, race build, migration bootstrap, Docker lab config and Python
  compilation passed. Five Docker images built successfully (four candidate
  services plus baseline Dispatcher). Hosted CI was not run.
- Application statement coverage: 26.5%
  (490/1846), excluding generated/vendor paths only. Docker/native
  subprocess execution is not included in coverage. Diagnostic, not a resume claim.
- An initial harness run expected HTTP 200 for lease creation; the actual API
  correctly returned 201. The harness was corrected and the full reliability run
  passed. Its original failure and service logs are retained under `failed-attempt-1`.
- Earlier same-day Buf/frontend/promtool tests remain in the
  [preceding report](../2026-09-07/report.md); those unchanged components were not
  rerun here. Live Prometheus evaluation was exercised in this follow-up.

## Reproduction and artifacts

Follow the [lab runbook](../../runbook.md#isolated-docker-recovery-and-latency-lab).
Use `scripts/build-docker-lab.py --baseline <extracted-baseline>` and
`scripts/verify-docker-lab.py --phase all --output <new-output-directory>`.
Source base commit `84206e198423529c244ef4ac12072230a004e8df` is old local HEAD; current code and
baseline both include local changes. Do not claim a permalink or hosted CI run.

Raw archive: `/home/postedism/Desktop/Find work/jd-resume-tailor/backups/fleetrelay-docker-20260907T232637Z-675d445b/verification`.
It retains original per-command `performance.json`, runtime statistics, images/
build logs, service/fault logs, journals, the logical dump, test JSON, coverage,
failed harness run and exact starting worktree. `summary.json` records hashes.
Cleanup removes only the explicitly named disposable lab and regression projects.

Unfilled evidence remains: physical actuator/real power-loss behavior, cloud
services and orchestration, managed cluster/region failover, production traffic,
SLO/load gates, full OTLP, production identity/TLS/mTLS, hosted CI and publication.
The local container controls and recovery exercises are useful infrastructure
experience, but do not change those scopes into completed claims.
