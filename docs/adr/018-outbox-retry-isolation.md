# ADR-018: Durable outbox retry isolation and backlog visibility

- Status: Accepted for the local prototype.
- Date: 2026-09-07.
- Extends ADR-017's publication recovery. Production ADR-014 remains Proposed.

## Problem and decision

The prior single Dispatcher repeatedly selected the oldest 64 unhandled rows.
A batch of malformed payloads or unavailable destinations could monopolize that
selection, starving later healthy work. Merely retaining an intent proves safety,
not progress. The regression injects 64 JSONB delivery envelopes of the wrong Go
type followed by a healthy fleet event; three old-style batches never publish it.

Migration 2 adds `publication_failures` and `next_publish_at` plus a partial
index on `(next_publish_at, created_at, outbox_id)` for unhandled rows. Existing
rows inherit `created_at` as their eligibility time. The due-work read uses
PostgreSQL statement time and orders eligibility before creation time. A failed
row moves behind already-eligible work, retaining its UUID and payload.
`ClaimOutbox` is a historical method name; it still does not acquire durable
multi-worker claims. The advisory single-Dispatcher boundary remains.

Each publication plus completion call has a two-second context. Redis honors that
context, uses explicit two-second I/O/dial limits and disables nested client
retries. A failure persists equal-jitter exponential delay: 0.5–1, 1–2, 2–4,
4–8, 8–16, then 15–30 seconds. These are policy constants, not measured SLAs.
The database assigns retry time using its clock. A compare-and-set failure count
prevents stale batch results from postponing a newer retry or reviving a handled
row. The SQL counter saturates at the integer limit. It is diagnostic retry
state, not a device attempt count.

Redis success followed by failed PostgreSQL confirmation is still ambiguous.
The completion transaction rolls back; its original envelope is retried with the
same delivery ID. Multiple Redis entries are allowed. Pending delivery intents
continue to suppress additional device attempts; ACK timing still begins at
confirmed publication. No error handler deletes or falsely completes bad work.
Cancellation stops processing untouched rows, and deferral uses the parent
context rather than an already-expired item context. If database deferral itself
fails, the batch exits with an error and the pending row remains eligible.

## Visibility and costs

The Dispatcher directly exposes three label-free committed-state gauges:
`fleetrelay_outbox_pending`, `fleetrelay_outbox_retry_waiting`, and the existing
`outbox_oldest_unpublished_seconds` name. Delayed rows count toward backlog and
age. `published_at` continues to mean handled, including deliberately retired
obsolete intents. Database collection has a one-second timeout; an error returns
HTTP 503 without fabricated zero samples. Prometheus then marks the scrape down.

The age alert now has a producer; delayed-retry alert rules and the Command
Delivery dashboard use the same series. Other ACK, consumer-lag and telemetry
panels remain scaffolding. This is direct Prometheus exposition, not OTLP tracing.

Costs include a write and index maintenance per failed publication, pending-row
scans on scrapes, another schema migration and up to the backoff ceiling before
recovery is retried. A large backlog can time out metric collection. Batches still
share a 15-second outer budget and one worker; finite-fixture progress is not
strict tenant fairness, a throughput bound or HA certification. The deadline
worker's separate candidate scan was not redesigned. There is no DLQ or poison
payload repair tool; malformed rows remain durable and visible for investigation.

## Compatibility and rollout

The database change is additive. Run the Compose migrate service before the new
Go services, including on existing data volumes. `migrations/apply.sql` serializes
known-version bootstrap with a transaction advisory lock and a five-second lock
acquisition timeout. It handles only this prototype's two known schema versions;
it is not a general-purpose migration framework or large-table online migration.
The old Dispatcher ignores eligibility even with the new columns, so replacing
it is required to obtain retry isolation. No mixed-version operation is certified.

For code rollback to the September 6 delivery-intent implementation, stop the new
Dispatcher first and retain the added columns. Removing migration 2 is optional
and loses retry diagnostics/schedules, though it preserves outbox payloads and
completion markers. Stop all new-version services before a down migration: new
store writes require the new columns. Rolling back before ADR-017 still requires
its delivery-intent drain/retirement procedure. Do not delete pending work.

## Verification and boundaries

[Executed report](../evidence/2026-09-07/report.md): real PostgreSQL and Redis,
identical starvation oracle against a starting-worktree copy, storage reopen,
stale/handled deferral, cancellation, ACK budget, failed completion after real
XADD, per-item timeout isolation, database metrics, migration scripts and alert
rules. The real-process simulator path also reads the Dispatcher metrics endpoint.
A hook models a stalled XADD; reopen models lost process-local state. Neither is
a physical network partition or machine crash. No physical robot, production
traffic, cloud deployment, GPU workload or performance improvement is claimed.
