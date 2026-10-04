# ADR-017: Recoverable publication for prototype device attempts

- Status: Accepted for the local prototype scope; ADR-014 remains Proposed.
- Date: 2026-09-06
- Scope: PostgreSQL Store and single active Dispatcher. No organizational or production approval implied.

## Problem and decision

Previously `CreateAttempt` committed before Redis XADD. An unavailable Redis or a
lost response could leave an attempt without a recoverable envelope, and replay
of `command.created` could create more attempts. The device retry budget counted
transport uncertainty as another device attempt.

Reuse the existing outbox table and its unique business key. The command row and
existing per-device lock protect creation of an attempt plus `command.delivery`
row in the **same transaction**; that transaction also marks `command.created`
handled. The business key is `delivery:<command_id>:<attempt>`; the UUID outbox ID
is the stable `delivery_id`. The immutable payload contains command and attempt.
No schema migration or new Go library is required.

Publication retries XADD with the same envelope, attempt and delivery ID. Redis
Stream IDs may differ. After success, one PostgreSQL transaction marks the intent
handled and sets the attempt's three-second ACK deadline from confirmed
publication. Duplicate confirmations do not move the deadline. Unpublished
intents suppress new attempts; command expiry can still resolve pending state.
Before publication, PostgreSQL preflight rejects terminal, expired, superseded
session/attempt and released-lease intents. These are retired using the existing
outbox completion marker; `published_at` therefore means **handled**, including
obsolete intents that were deliberately never sent.

## Crash boundaries

| Boundary | Durable state | Recovery |
|---|---|---|
| Attempt transaction fails before commit | Neither attempt nor intent; created work still pending | Retry first scheduling |
| Commit succeeds, caller response is lost | Attempt and intent exist; created work handled | Read and publish existing intent |
| XADD fails before Redis accepts | Intent still pending | Retry same delivery |
| XADD succeeds, response or PostgreSQL completion is lost | Redis may contain a message; intent pending | Republish same delivery ID; duplicates permitted |
| Completion commits | Intent handled; ACK timer established | Bounded deadline worker can schedule the next device attempt |

## Alternatives and costs

Keep direct XADD: fewer database writes, but leaves the demonstrated gap.
Use a Redis transaction: cannot atomically commit PostgreSQL business state.
Build the full production work/claim/permit subsystem now: addresses additional
HA concerns, but also needs tenant, ownership, scheduler-term and device-protocol
changes. This patch takes only the atomic-intent portion of ADR-014.

Costs: another outbox row per device attempt, an additional polling hop (up to the
nominal 250 ms loop interval before processing delay), retained payload storage,
and duplicate delivery on ambiguous publication. No performance improvement is
claimed. Pending-outbox indexing, retention, poison-row isolation, metrics and
scalable worker claims still need dedicated work. A failed batch of 64 old rows
can delay newer work; this patch does not introduce a fair scheduler or DLQ.

## Compatibility and rollback

Existing Gateway and simulator fields are unchanged; extra Redis `delivery_id`
and `attempt` fields are additive. The prototype wire protocol still lacks an
attempt-specific dispatch permit. Preflight is not atomic with gRPC execution,
and persistent device deduplication and exactly-once execution remain unproven.

The **old Dispatcher must not consume new `command.delivery` rows**: it treats
unknown kinds as fleet events. Pause command admission, let old in-flight work
finish/expire, stop the old Dispatcher, then start the new one. There is no mixed
Dispatcher rollout claim. To roll back, keep the new Dispatcher until its
delivery intents are drained/retired, stop it, then restore old code. Keep
PostgreSQL data; do not delete pending intents. These operational instructions
are documented procedures, not a tested live rollback campaign.

## Verification

[2026-09-06 evidence](../evidence/2026-09-06/report.md) records real PostgreSQL and
Redis tests, 16 concurrent creators, deterministic failures before/after XADD,
storage close/reopen, expiry/fences, a real four-process simulator command path,
and an atomicity regression that fails on `84206e1`. Hook-injected response loss
and connection reopen are not represented as OS SIGKILL or multi-node failover.
