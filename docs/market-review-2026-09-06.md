# FleetRelay engineering priorities — 2026-09-06

## Context and limits

Reviewed the clean starting tree at `84206e1`, both commits, README, Go services,
tests, deployment configuration and production ADRs. The current banks were found
initially under `/home/postedism/Desktop/Find zwork/jd-resume-tailor/data/`.
During this task the parent was renamed to **`/home/postedism/Desktop/Find work/jd-resume-tailor/data/`**; the banks were relocated and reread. Neither initially contained
FleetRelay at the initial read. The experience bank uses an `experiences` array;
the evidence bank retains schema `1.0`, a `projects` map and an `evidence` array.
Local Codex history/session files were accessible as leads. A home-directory
filename search and historical mentions did not locate an accessible
`Project Experience Bank Generator.md`; no dedicated memory directory or
conversation-search connector was available. That generator was not applied.
Current files take precedence over historical macOS paths and old test claims.

The existing co-op entry supports an early-career search, but graduation date and
US work authorization are not established by these files. This is a capability
comparison, not confirmation of eligibility for a particular hiring cohort.

## Official hiring sample

Checked **2026-09-06**. Three current official samples are deliberately small and
non-representative. Publication dates below come from the employer's ATS metadata
when available; crawler timestamps are not treated as publication dates.

| Role / location | Publication and current-list evidence | Relevant signal and scope |
|---|---|---|
| [Zettabyte Software Engineer (New Grad), US / Palo Alto](https://jobs.ashbyhq.com/zettabyte-space/50a9c6dc-7225-4ff0-a914-69d1dd325d6a) | `publishedAt` 2026-04-08; listed in [official API](https://api.ashbyhq.com/posting-api/job-board/zettabyte-space) | 0–2 years; Go services, Linux/networking, deployment, CI/CD, monitoring and deliberate failure testing. Kubernetes operations are central to this particular role. Mandarin and gRPC/message queues are bonuses. Requires work authorization without sponsorship; eligibility unknown here. |
| [Hipp Software Engineer – New Grad, US remote](https://jobs.ashbyhq.com/hipp/ea04e914-674c-4c05-b40a-7e8a035bad14) | `publishedAt` 2026-06-18; listed in [official API](https://api.ashbyhq.com/posting-api/job-board/hipp) | Intern/new-grad scope; backend APIs, tested maintainable code, CS fundamentals and ownership. Cloud, databases and distributed systems are nice-to-haves; Go is one accepted language. Healthcare domain is adjacent to the co-op background. |
| [Stripe Software Engineer, New Grad, New York / Seattle / South San Francisco](https://stripe.com/careers/listing/software-engineer-new-grad/8128744) | Official page readable on check date; publication date not disclosed, so **not asserted to be posted within six months** | Degree by summer 2027 and ≤18 months professional experience excluding internships. Fundamentals, collaboration and written explanation; code review and safe production changes preferred. Go is one of several languages, not a universal hiring requirement. |

Two useful discovery results did not survive current-list validation:
[Foxglove New Grad](https://jobs.ashbyhq.com/foxglove/def61478-8b86-43e5-b27b-be7b76900449/)
described device connectivity and robotics data infrastructure, and
[Benchling New Grad 2026](https://jobs.ashbyhq.com/benchling/b3c9b312-6e2b-4dbc-9b15-0b0310d75a7f)
described platform reliability. Neither ID appeared in its current public ATS
listing. They are historical context, excluded from current-sample counts; exact
posting/closure dates are unknown. A Compa current listing did not confirm its
search-indexed new-grad result either, so it was not used as a current sample.

[Abridge Model Inference Infrastructure](https://jobs.ashbyhq.com/Abridge/c7f09dee-07ca-444f-803d-961e7f88056b/)
asks for 2+ years deploying ML models in production plus Kubernetes expertise.
It is a stretch-role comparison, not evidence that this candidate already has
production ML experience. FleetRelay should demonstrate transferable control-plane
reliability; VelaServe remains the more direct ML-infrastructure project.

**Inference from this sample:** programming fundamentals, ownership and code
quality recur across the three current roles. Failure handling and operational
evidence provide a concrete way to demonstrate those skills. Strong Kubernetes
administration, a particular queue, or GPU tuning are not universal requirements
in this sample. No job explicitly prescribes this project's outbox implementation.
The implementation choice follows from its code defect, informed by those signals.
ATS dates/status and response hashes are in
[research-sources.json](evidence/2026-09-06/research-sources.json).

## Ecosystem and version decision

Official repositories/releases were checked on **2026-09-06**. Recent activity is
evidence of maintenance, not a guarantee that a version is vulnerability-free.

| Component | Verified official material | Decision, benefit and compatibility cost |
|---|---|---|
| Go | [Release history](https://go.dev/doc/devel/release), [1.26 notes](https://go.dev/doc/go1.26): 1.26.8 and 1.27.1 released 2026-09-01 | Pin minimum toolchain and Docker build default to **1.26.8**, matching the executed runtime. This preserves the existing supported 1.26 line and includes its accumulated fixes; no need for a 1.27 feature migration. Builders below 1.26.8 must update. CI reads go.mod. No performance gain attributed to pinning. |
| pgx | [Official repository/changelog](https://github.com/jackc/pgx/blob/master/CHANGELOG.md): stable 5.10.0 dated 2026-06-03; 5.7.6 dated 2025-09-08 | Keep 5.7.6 for this bounded transaction change. 5.10 adds protocol/authentication hardening worth a separate upgrade with cancellation, TLS and pool regression tests. The latest GitHub release endpoint returned 404; the changelog, not that endpoint, establishes the stable version. The parser/time-format changes under **Unreleased** are not stable-release features. No claim that 5.7.6 is security-current. |
| go-redis | [v9.22.0 release](https://github.com/redis/go-redis/releases/tag/v9.22.0), 2026-08-03; [official README and upgrade guidance](https://github.com/redis/go-redis) | Already on the observed stable release. Its release notes flag default timeout/backoff changes and experimental caching/autopipelining. Keep the current version and existing Streams APIs; do not enable experimental throughput features without a relevant benchmark. |
| gRPC-Go | [v1.83.2 release](https://github.com/grpc/grpc-go/releases/tag/v1.83.2), 2026-08-25; [repository](https://github.com/grpc/grpc-go), [version/behavior notes](https://github.com/grpc/grpc-go/releases) | Keep 1.75.1 within this delivery patch. Newer versions include security/transport fixes (1.83.2 rejects missing authority/Host); upgrading eight minor versions warrants dedicated stream/reconnect and transport tests. xDS/RBAC changes do not describe the current plaintext static-token deployment. Follow-up dependency review remains open; this is not a vulnerability audit. |
| coder/websocket | [v1.8.15 release](https://github.com/coder/websocket/releases/tag/v1.8.15), 2026-06-15 | Already on observed stable version; compression framing fix is included. No replacement library needed. |

No database major upgrade, Kubernetes migration or extra message platform is
needed to make PostgreSQL attempts and publication intent commit atomically.
PostgreSQL 17.6 and Redis 8.2 are the existing local test images; this task does not
certify the maintenance/security status of every image or transitive dependency.

## Gap comparison and priorities

| Priority | Gap and engineering value | Portfolio comparison | Acceptance |
|---|---|---|---|
| P0 — implemented | Attempt could commit without recoverable Redis delivery intent; publication errors could burn device retry budget. Persist an immutable intent with each attempt and replay its stable ID. | AsyncFlow already covers general leases/worker scheduling; its initial bank explicitly lacked a publisher. FleetRelay adds actual PostgreSQL→Redis transport and device-session boundaries. | Forced insert failure rolls back attempt + intent; 16 concurrent creators yield one attempt/intent; reopen and repeated failed XADD retain identity; lost reply allows duplicate Stream messages but one attempt. |
| P1 — implemented and locally verified | Previous PostgreSQL/Dispatcher packages had no automated integration tests. Add isolated real dependencies, strict CI integration gates and a four-process simulator smoke test. | Complements existing infrastructure artifacts with executed data-plane evidence; no duplicate Helm/Terraform exercise. | Race tests, Go vet/build, contract checks, frontend checks, Compose parsing, simulator success and a failing old-revision negative control. |
| P2 — deferred | Production claims/leases, durable browser replay, authorization/TLS, persistent device execution journal, instrumentation and measured load tests. | Preserve FleetRelay's device-control focus and VelaServe's ML focus. | Requires separate protocol/security work and evidence; none are silently upgraded to implemented. |

This change makes a correctness/recoverability claim. It does **not** claim lower
latency, higher throughput, exactly-once execution, production HA or cloud/GPU use.
