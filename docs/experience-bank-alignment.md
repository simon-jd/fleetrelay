# Experience Bank implementation map

Result provenance: owner-confirmed separate cloud-hosted test results. The README reproduces the current selected bullets. Commands below exercise this checkout; new outcomes must be recorded separately from those supplied results.

| Result | Implementation and regression evidence | Reproduce | Scope / source difference |
|---|---|---|---|
| 1 | [internal/control/postgres/store.go](../internal/control/postgres/store.go) · [internal/control/postgres/delivery_integration_test.go](../internal/control/postgres/delivery_integration_test.go) | `go test -race ./internal/control/postgres` | Atomic attempt and outbox rollback and concurrent command fences. |
| 2 | [cmd/dispatcher/outbox.go](../cmd/dispatcher/outbox.go) · [cmd/dispatcher/outbox_progress_integration_test.go](../cmd/dispatcher/outbox_progress_integration_test.go) | `make test-integration` | Persisted eligibility is named next_publish_at in this source; malformed items remain durably deferred, allowing healthy rows to progress. |
| 3 | [scripts/verify-docker-lab.py](../scripts/verify-docker-lab.py) · [cmd/dispatcher/outbox_test.go](../cmd/dispatcher/outbox_test.go) | `python3 scripts/verify-docker-lab.py --phase all` | Five-pair cloud outcome is separate from the older checked-in Docker latency round. |
| 4 | [cmd/device-sim/journal.go](../cmd/device-sim/journal.go) · [cmd/device-sim/journal_test.go](../cmd/device-sim/journal_test.go) | `go test -race ./cmd/device-sim` | Journal persists simulated results and fences before ACK; no physical actuator guarantee. |
| 5 | [scripts/verify-docker-lab.py](../scripts/verify-docker-lab.py) · [docs/evidence/2026-09-07-docker/report.md](../docs/evidence/2026-09-07-docker/report.md) | `make test-integration` | Native dependency oracles and Docker recovery/restore are separate acceptance layers. |

## Measurement boundaries

- The 49.1% comes from a single-dispatcher, phase-controlled trace (arrival about 239 ms after the last scan, with the baseline waiting about 261 ms for the next scan). Do not present it as a pooled p95 improvement across all requests, a throughput gain, or a universal constant; a uniform-random-arrival experiment measured p95 747 to 276 ms.
- Multi-dispatcher contention (locks, leases, queue jitter) was not tested; do not extrapolate the single-instance latency ratio.
- No unconditional exactly-once for physical actuators: there is no universal atomic commit for physical actions. Only simulated effects were shown recoverable from the replayable log, with exactly one result per command ID; real devices need idempotent actions, sensor feedback, or hardware transactional support.
- Immediate follow-up runs only after the database commit and only speeds up lookup of committed intents; the outbox and the periodic 500 ms scan still own recovery, and nothing is dispatched before commit.
- The eight-scenario, seven-table recovery drill proves logical recovery for that Docker input only; it is not evidence for cross-region failure, managed-database recovery time, or real device state consistency, and it does not prove cloud DR.
- The historical 531.56 to 278.16 ms result (47.67%) belongs to a different round; keep the two rounds separate and do not mix them into this round's statistics.
- Environment scope: one Linux Docker host (8 vCPU/16 GiB), PostgreSQL 16, Redis 7, one dispatcher, four gRPC device simulators; no production, cloud, or physical-device evidence.

## Local verification

See `docs/alignment-verification.json` for commands and outcomes from this checkout. Supplied cloud numbers, historical checked-in artifacts and new local checks are separate evidence sets. A skipped dependency test is not a pass.
