# FleetRelay priorities — 2026-09-07

## Context restored

Current repository: `/home/postedism/Desktop/Go FleetRelay`, branch `main`, local
HEAD `84206e198423529c244ef4ac12072230a004e8df`. The starting tree already contained
the September 6 delivery-intent work; this update preserves and extends it.
Both local commits, service/store code, tests, schema, Compose/CI and production
ADRs were inspected. No applicable AGENTS.md was found in the repository or its
ancestor directories. The production specifications remain targets.

The current banks are `Desktop/Find work/jd-resume-tailor/data/experience_bank.json`
and `project_evidence_bank.json`; their existing FleetRelay ID is
`project-jdinseattle-fleetrelay`. The supplied
`/home/postedism/Downloads/Project Experience Bank Generator.md` was available and
read. Its older macOS paths and suggestions to invent production/results are
superseded by the user's current paths and explicit evidence-only instructions.
The experience array and evidence schema 1.0 are preserved.

Local history.jsonl and the read-only thread-history database were accessible.
Historical messages locate the banks and mention a later remote history rewrite;
they do not establish the current remote contents or individual authorship.
Local Git has two commits and no `origin/HEAD` symbolic ref. `git ls-remote` failed
on SSH configuration permissions, so the remote default branch/publication were
not verified. No new-code permalink points at the old HEAD. No dedicated memory
search connector was available; current files govern all claims.

The current work entry describes Software Engineer experience since December
2025 in a laboratory platform. This supports an early-career comparison, without
assuming a graduation year, internship classification, or work authorization.

## Fresh official hiring checks

All pages below were checked September 7, 2026 (local date). For the two Ashby
roles, the public employer ATS API was fetched again and confirmed the job ID and
`publishedAt`. Crawler timestamps are not publication dates. The web reader could
not open the ATS JSON; a direct read-only HTTPS fetch succeeded.

| Official role | Publication / location | Relevant requirements |
|---|---|---|
| [Zettabyte — Software Engineer (New Grad)](https://jobs.ashbyhq.com/zettabyte-space/50a9c6dc-7225-4ff0-a914-69d1dd325d6a) | ATS 2026-04-08; US, Palo Alto hybrid; 0–2 years | Go backend services, testing and deliberate failures, deployment, monitoring, runbooks and end-to-end ownership. Kubernetes/Linux/networking operations are prominent here. Sponsorship eligibility is not established for the candidate. |
| [Hipp Health — Software Engineer, New Grad](https://jobs.ashbyhq.com/hipp/ea04e914-674c-4c05-b40a-7e8a035bad14) | ATS 2026-06-18; US remote | APIs, maintainable tested code, CS fundamentals, reviews and project ownership. Cloud, distributed systems and databases are nice-to-haves. The healthcare context is adjacent to the current work entry. |
| [Stripe — Software Engineer, New Grad](https://stripe.com/careers/listing/software-engineer-new-grad/8128744) | Page readable; posting date undisclosed; New York/Seattle/South San Francisco | Fundamentals, written explanation, collaboration, validation of AI-assisted code and safe system changes. Degree by summer 2027 or equivalent; no more than 18 months professional experience excluding internships. |

The first two are within six months; Stripe is **not** asserted to be a recent
posting. An indexed [Eventual new-grad role](https://jobs.ashbyhq.com/eventualcomputing/becb5675-3480-4d2b-b126-2acad40fd088/)
was absent from the freshly fetched employer ATS listing and is excluded from the
current sample. API URLs, timestamps, hashes and compact findings are preserved
in [research-sources.json](evidence/2026-09-07/research-sources.json).

**Inference, not a market-wide statistic:** tested maintainable code, explanation
and ownership recur in this small sample. Operational failure handling gives this
project a concrete way to demonstrate them. Kubernetes depth, a particular queue
or GPU execution is not a universal requirement in these three jobs. None asks
specifically for this outbox algorithm. AI branding does not justify recasting a
device-control prototype as a model-serving deployment.

## Official ecosystem review and version decision

| Component | Checked official release / guidance | Decision and cost |
|---|---|---|
| Go | [Release history](https://go.dev/doc/devel/release): 1.26.8 and 1.27.1, 2026-09-01; two-major support policy | Keep the existing 1.26.8 pin and executed toolchain. No 1.27 feature is needed; no speedup is attributed to a version. |
| pgx | [Repository/version policy](https://github.com/jackc/pgx), [changelog](https://github.com/jackc/pgx/blob/master/CHANGELOG.md): 5.10.0, 2026-06-03 | Keep 5.7.6 for the queue patch. 5.10.0 adds authentication/protocol hardening and cancellation fixes; a separate upgrade should test connection cancellation, pooling and TLS. Stable v5 APIs support this SQL change. Unreleased parser changes are not counted as stable features. No security-current claim for the retained version. |
| go-redis | [v9.22.0](https://github.com/redis/go-redis/releases/tag/v9.22.0), 2026-08-03; [README upgrading guidance](https://github.com/redis/go-redis#upgrading) | Already on observed stable. Release notes change defaults and introduce experimental caching/pipelining. Set explicit Dispatcher I/O deadlines, honor contexts and disable nested client retries so the persistent scheduler controls retry cadence. Keep existing Streams APIs; do not enable experimental features. |
| gRPC-Go | [v1.83.2](https://github.com/grpc/grpc-go/releases/tag/v1.83.2), 2026-08-25 | Keep 1.75.1 in this bounded change. The observed release includes request-authority hardening; migration across the intervening versions still needs stream/reconnect/transport compatibility checks. No blanket vulnerability audit claimed. |
| PostgreSQL / Prometheus | [ALTER TABLE](https://www.postgresql.org/docs/17/sql-altertable.html), [text exposition](https://prometheus.io/docs/instrumenting/exposition_formats/) | Use additive SQL columns/index and the existing scrape endpoint. Migration locks and pending-index scans have costs. No new broker, database major version or instrumentation framework is required. |

The pgx, go-redis and gRPC repositories were unarchived with recent upstream
activity when queried; maintenance activity is not a guarantee of security.
The dependency versions and lockfiles are unchanged by this September 7 update.

## Priorities and portfolio comparison

| Priority | Gap / engineering value | Acceptance |
|---|---|---|
| P0 | Failed old rows monopolize the 64-row outbox batch. Persist eligibility and transport failures; isolate per-item timeouts. | Identical regression fails on starting worktree; 64 malformed rows remain recoverable while a later healthy event reaches real Redis within three batches. Restart preserves delay; stale failure cannot revive handled work. |
| P1 | Existing backlog alert had no real application series. Export committed pending count, delayed count and oldest age. | Real database/HTTP assertions, scrape failure on unavailable database, alert-rule tests, real-process metrics after simulator completion. |
| P1 | Existing-volume bootstrap only knew migration 1. Add guarded, serialized migration application and rollback checks. | Fresh, repeat and existing-row upgrade plus down/up SQL checks; full race regression and simulator path. |

AsyncFlow already covers worker scheduling and tracing; MaterialFlow covers
transactional Kafka recovery; Quorum Market covers distributed storage; VelaServe
and the GPU labs provide direct ML systems evidence. FleetRelay's incremental
value is publication failure isolation within fenced device-command delivery.
This is a correctness and operability update. It is not a measured latency,
throughput, availability or production-impact improvement.
