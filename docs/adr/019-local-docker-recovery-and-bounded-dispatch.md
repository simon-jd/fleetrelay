# ADR-019: Local crash recovery evidence and bounded dispatch follow-up

Status: Accepted for the simulator/prototype only. Date: 2026-09-07.

## Context

The PostgreSQL delivery-intent transaction protects publication recovery, but
the simulator's process-local cache could not survive a restart. The Dispatcher
also waited for a second 250 ms polling tick to publish a newly created intent.
Backend/infrastructure interviews benefit from reproducible fault and latency
evidence; neither a design document nor a Docker deployment establishes physical
device safety or production experience.

## Decision

When `DEVICE_JOURNAL_PATH` is set, the simulator exclusively locks that journal
and persists command fingerprints, results, sequence/lease fences and a simulated
effect counter. A temporary snapshot is fsynced, renamed and its directory fsynced
before acknowledging success. Invalid data, a second owner or an ambiguous write
failure prevents further execution. Fingerprints exclude session generation so
the same command can be replayed after reconnecting to a replacement session.
Changed content under an existing command ID is rejected. The journal is bounded
at 10,000 records and stops accepting new executions when full; it has no GC.

The simulated effect is an increment in the **same snapshot** as the result.
This is not atomicity between a filesystem and a physical actuator. ADR-016's
production journal, vendor operation tokens, UNKNOWN outcome and GC remain
proposed. Deleting the journal/volume loses deduplication history. The existing
ephemeral mode remains available when no path is configured.

After successfully processing a `command.created` row, the Dispatcher performs
at most one additional 64-row due-work pass within the same outer deadline. It
reads committed intents through the normal PostgreSQL path, retains stable
delivery IDs, and uses the same retry/backoff and confirmation handling. There
is no direct send of an uncommitted in-memory envelope. A tick can examine at
most 128 rows; the second pass can still encounter older eligible work, so this
does not guarantee immediate delivery or fairness under backlog.

## Alternatives and costs

- Reducing the global tick interval increases idle database polling. A bounded
  follow-up avoids the additional idle reads, but adds a query after handled
  created events and may extend that tick's work.
- Bypassing the durable outbox reduces code-path latency at the cost of a new
  failure boundary. Reusing the committed path keeps the existing safety checks.
- An embedded database would support journal retention better. A bounded JSON
  snapshot keeps this simulator inspectable, but copies and rewrites all cached
  results per execution. Large-journal capacity/performance is not certified.
- Kubernetes/cloud provisioning does not resolve missing production identity or
  device contracts. This change uses isolated local Docker services without
  asserting a cloud rollout or managed-service failover.

## Rollout, rollback and verification

No Go dependency or database schema changes are required beyond ADR-018. Create
a writable journal directory owned by the non-root service user and mount a
durable volume; other application paths can remain read-only. The fault-only
`SIM_EXIT_AFTER_PERSIST=true` option requires a journal and exits with code 86
after a new result is persisted but before its ACK. It must not be enabled for
normal simulator operation.

Rolling the Dispatcher back only restores the extra polling wait. Rolling the
simulator back to an ephemeral cache discards restart deduplication guarantees;
stop command submission and retain the volume instead of assuming the older
binary can interpret this journal. Schema rollback still follows ADR-018.

The [Docker report](../evidence/2026-09-07-docker/report.md) records actual
SIGKILL/pause/restart, logical restore and live alert observations, plus alternating
baseline/candidate latency runs. Unit tests exercise concurrent duplicates,
reopen, conflicting content, corruption, file ownership, cancellation and fences.
A real PostgreSQL/Redis regression checks publication of the committed intent
within one batch call and fails against the pre-optimization Dispatcher.
