# Phase 0/1 executable acceptance specifications

状态：Proposed。实现测试时允许补充命令和平台适配，但不得弱化 oracle、forbidden outcomes 或 threshold。所有时间使用 PostgreSQL/测试控制器记录的单调时间，所有资源使用唯一 run_id 前缀。

## PR-DOM-001 — 领域与并发不变量

- mode：A；每 PR；Phase 0 运行模型/DDL/legacy-tenant 子集，Phase 1 运行 forced-RLS 与完整运行时子集。
- version：候选 Git SHA、migration hash、Go module sum。
- topology：race-enabled Control API ×3、临时 PostgreSQL、Redis；每次 clean schema。
- workload：2 tenants × 20 devices；每 device 32 并发 lease/command/ACK；10,000 次属性序列。
- seed：CI 固定记录；失败 seed 必须成为 regression corpus。
- precondition：migration up 完成；Phase 0 使用 legacy tenant 与目标 schema model，Phase 1 才要求 RLS forced；测试 clock endpoint 禁用在非测试 build。
- trigger：随机交错 acquire/revoke/expire、同/异 hash idempotency、session replacement、valid/stale/late/conflicting ACK。
- expected transitions：仅 ADR-013 合法转换；sequence/epoch 单调；相同 key/hash 返回同 command。
- forbidden outcomes：跨 tenant 命中、双 active lease/work、相同 sequence、终态静默覆盖、race detector 告警。
- metrics/threshold：10,000 序列全过；非法成功=0；race=0；serialization/deadlock 最终未收敛=0。
- timeout：单 seed 60 秒，全场 20 分钟。
- cleanup：按 run_id 删除 Redis/DB，失败前先导出。
- artifacts：JUnit、seed、SQL invariant dump、race log、trace。
- owner：Control Plane；reviewer：Data。

## HA-PG-001 — Commit boundary ambiguity

- mode：A；nightly；Phase 0 exit。
- version：候选 image digest、migration hash。
- topology：Control API ×3、真实 PostgreSQL、Redis；网络 fault proxy。
- workload：Phase 0 每个当前已实现边界 200 command，覆盖 accept、attempt/outbox、ACK；Phase 1 增加 permit、MarkDispatched/auth/event 边界。
- seed：fault offset 与连接随机 seed 写入 manifest。
- precondition：基线 invariants=0，idempotency client 持久保存 key/receipt。
- trigger：在 transaction request、commit flush、commit response 前后断双向连接并重连。
- expected transitions：事务全成或全败；client 用原 key 查询；已提交 ACK 同时有 audit/event。
- forbidden outcomes：双 command、orphan attempt/outbox、无事件终态、错误 SUCCEEDED。
- metrics/threshold：2 分钟内每目标 command 可唯一判定；accepted 丢失=0；orphan=0。
- timeout：每 fault 3 分钟，全场 30 分钟。
- cleanup：恢复网络，导出后删除 run tenant。
- artifacts：proxy timeline、client receipts、tx/WAL sample、table diff、trace。
- owner：Reliability；reviewer：Control Plane。

## HA-DEL-001 — Delivery crash windows

- mode：A；nightly；Phase 1 exit。Phase 0 只运行 ADR-013/014 状态模型和 crash 后置条件测试，不要求完整 authorization 运行时。
- version：候选 images、proto digest、ADR-013/014 revision。
- topology：Dispatcher ×3、Gateway ×3、Control API ×3、PostgreSQL、Redis、具 persistent journal 的 simulator。
- workload：每个 safety class、每个 kill point 100 commands；同一 delivery 最多重领 10 次。
- seed：kill timing/worker/command seed 记录。
- precondition：Gateway capability v2、workload mTLS、device feature bits 已验证。
- trigger：在 attempt+outbox commit、XADD、PUBLISHED、permit commit、gRPC write、MarkDispatched、ACK commit 前后 SIGKILL 当前 owner。
- expected transitions：permit 前复用 delivery；permit 后 QUERY_ONLY/已证明 REPLAY_SAFE 可重试，RECONCILABLE 查询，NON_RETRYABLE/SAFETY_CRITICAL 进入 UNKNOWN；ACK 可从有效 POSSIBLY_SENT 对账。
- forbidden outcomes：新 attempt 由 publish retry 产生、nonce 并行签发、危险命令盲重发、PEL 被当作未执行证据、冲突结果覆盖。
- metrics/threshold：目标 command 在触发后 5 分钟内无超期 active READY/LEASED/PEL；accepted 丢失=0；支持幂等设备重复业务副作用=0。
- timeout：单点 5 分钟，全场 90 分钟。
- cleanup：恢复副本，等待 janitor，导出目标 command 全图后清理。
- artifacts：command/work/attempt/auth/outbox/event/PEL dump、device journal、kill timeline、trace。
- owner：Control Plane；reviewer：Device/Adapter、Reliability。

## HA-SCHED-001 — Scheduler term fencing

- mode：A；nightly；Phase 1 exit。Phase 0 只验证 claim SQL/锁顺序模型与 shadow claim。
- version：候选 images、schema hash。
- topology：Dispatcher ×3、PG primary/standby、network fault proxy。
- workload：10 tenants、1,000 active work、hot tenant 50% load。
- seed：partition owner 和 fault sequence。
- precondition：全部 worker 已记录 owner/term/lease；DB-time drift assertion=0。
- trigger：隔离 leader 的 DB response、等待 lease 过期、新 owner 接管，再恢复旧 leader；同时运行 deadline/reconcile/publisher。
- expected transitions：term 单调；新 owner 接管；旧 owner 所有条件写影响 0 行。
- forbidden outcomes：两个 active owner、旧 term attempt/event、逆锁顺序、未收敛 deadlock。
- metrics/threshold：handoff≤10 秒；duplicate work/attempt=0；触发后 60 秒 backlog 单调下降。
- timeout：10 分钟。
- cleanup：解除分区、等待 lease 回收、导出 partition history。
- artifacts：term/lease SQL、worker logs、attempt diff、lock graph、metrics。
- owner：Control Plane；reviewer：Reliability。

## SEC-TENANT-001 — Tenant isolation

- mode：A；每 PR 精简、RC 完整；Phase 1 exit。
- version：候选 digest、schema/RLS/policy hash。
- topology：共享 API/Gateway/PG/Redis，两个等规模 tenant，后台 worker 与连接池复用开启。
- workload：API/WS/cursor/GetDelivery/ACK/Redis key 各 10,000 个同租户不存在对象和跨租户对象随机请求。
- seed：ID、pool schedule、request order。
- precondition：应用角色非 owner/superuser/BYPASSRLS；forced RLS；日志脱敏。
- trigger：交换 tenant/device/delivery/cursor，伪造 Redis tenant_id，panic/rollback 后复用连接，以运维/后台角色访问。
- expected transitions：全部跨 tenant 请求与不存在对象返回相同 status/size；SET LOCAL 在事务结束清除。
- forbidden outcomes：数据读写、event/stream 泄漏、跨 tenant timing oracle、日志/指标出现原始他租户标识。
- metrics/threshold：越权成功=0；响应 status/size 差异=0；padding 后 p95 latency 比值 0.8–1.2；RLS context 污染=0。
- timeout：PR 10 分钟，RC 60 分钟。
- cleanup：导出安全审计后删除两个测试 tenant。
- artifacts：negative corpus、raw latency、RLS policy/role dump、Redis scan、redacted logs。
- owner：Security；reviewer：Data、Control Plane。

## REL-MIXED-001 — v1/v2 delivery mixed-version rollout

- mode：A；RC；Phase 1 exit。
- version：N/N-1 Gateway、Publisher 与 Control API image digests、proto digest。
- topology：v1-only Gateway、dual-read Gateway、v2-only canary stream、Publisher N/N-1、persistent simulator。
- workload：每版本组合 1,000 commands，包含滚动升级、回滚和 10,000 条旧 PEL。
- seed：instance placement、drain timing、PEL ownership。
- precondition：capability registration 可查询；v2 Gateway workload mTLS 有效。
- trigger：先双读、按 instance 切 v2、drain v1、清 PEL、禁止 legacy；中途在每阶段回滚。
- expected transitions：Publisher 只写兼容格式；同一 delivery 对单设备只选一种语义；旧 PEL 可判定退休。
- forbidden outcomes：v1 Gateway 收到 reference、v2-only stream 收到 payload、同一 command 两种 envelope 执行、无身份 GetDelivery。
- metrics/threshold：错误路由=0；重复业务副作用=0；drain 后 5 分钟旧 PEL=0；rollback≤15 分钟。
- timeout：90 分钟。
- cleanup：恢复目标版本、删除测试 stream/group、保留 journal manifest。
- artifacts：capability registry、stream/PEL dump、device journal、rollout/rollback timeline。
- owner：Edge；reviewer：Control Plane、Release。
