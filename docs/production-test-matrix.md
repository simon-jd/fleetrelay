# FleetRelay production acceptance matrix

状态：Proposed。任何场景只有生成独立 run_id 和全部必需证据后才算通过。

模式：

- A：自动化阻断门禁；
- M：人工演练并审批；
- C：生产持续监控/合成探针。

每个可执行场景定义必须包含：scenario_id、mode、代码/镜像/IaC 版本、topology、workload、seed、precondition、trigger、expected transitions、forbidden outcomes、metrics、threshold、timeout、cleanup、artifacts 和 owner。缺少任一字段视为测试规格未完成，而不是默认通过。

Phase 0/1 的逐步可执行规格见 [production-tests/phase-0-1.md](production-tests/phase-0-1.md)；本表是跨阶段门禁索引。

| Scenario | Mode/frequency | Topology / trigger | Oracle 与禁止结果 | 阈值 / timeout | 必需证据 / owner |
|---|---|---|---|---|---|
| PR-DOM-001 | A / 每 PR | 临时 PG/Redis；并发 lease、idempotency、状态机属性序列 | tenant/epoch/sequence 单调；非法转换、重复 command、冲突 hash 不得成功 | race=0；不变量失败=0 | JUnit、seed、DB snapshot / Control |
| HA-PG-001 | A nightly，M quarterly | 多 API；在 command/attempt/ACK/outbox commit 边界断连接 | 事务全成或全败；同 idempotency key 恢复；不得双 command | 2 分钟内状态可判定；错误成功=0 | timeline、WAL/tx sample、trace / Reliability |
| HA-PGF-001 | A RC，M quarterly | 目标 N-1 负载；托管 PG primary failover | 已提交事务 RPO=0；API/worker 重连且不变量通过 | RTO≤5m；随后 15m validation | provider event、commit-set diff、E2E / Reliability |
| HA-DEL-001 | A nightly | 在 attempt+outbox commit、XADD、PUBLISHED、permit commit、gRPC send、MarkDispatched 各点 kill | permit 前同 delivery 可重发；permit 后 QUERY_ONLY/已证明 REPLAY_SAFE 可重发，RECONCILABLE 查询，危险分类 UNKNOWN；nonce 不重复 | accepted 丢失=0；触发后 5m 内无超期 active READY/LEASED/PEL | command/work/attempt/auth/outbox/PEL dump / Control |
| HA-REDIS-001 | A RC，M quarterly | 目标负载；Redis 全量替换为空集群 | PG 事实不变；presence/event/delivery 重建；终态不重执行 | 实时层 RTO≤5m；压力场景 30m outage 后≤15m 清 backlog | before/after DB、Redis epoch、E2E / Reliability |
| HA-SCHED-001 | A nightly | 3 Dispatcher；partition leader 连接分区、lease 过期、旧 worker 恢复 | 旧 term 条件写失败；每 generation 最多一个 active work | 双写=0；handoff≤10s | term/lease SQL、attempt diff / Control |
| HA-GW-001 | A nightly | 三 AZ 同型；pause、SIGKILL、单向分区、同 ID 重启 | instance/session fencing；旧 presence/stream 不可执行 | 95% reconnect≤15s，99%≤30s | session/gateway/PEL/trace / Edge |
| EVT-REPLAY-001 | A RC | 100 reconnect、10k×≤1KiB events、20% filter；在 snapshot 事务捕获 durable high-watermark H；Gateway/Redis 切换和 buffer overflow | snapshot 与 H 同一视图；可见事件顺序正确；隐藏事件不泄漏；overflow 重做 | 99% run≤5s；持久事件缺失/越权=0 | offsets、cursor、client ACK、DB query / Realtime |
| SEC-TENANT-001 | A 每 PR/RC | 两 tenant；API/WS/cursor/Redis/RLS/后台任务/连接池复用；每类≥10k随机请求 | A 不能读写或推断 B；SET LOCAL 不跨事务泄漏；同/跨租户不存在对象响应同 status/size | 跨租户成功=0；padding 后 p95 latency 比值 0.8–1.2 | negative cases、RLS audit、raw latency / Security |
| SEC-ID-001 | A RC，M quarterly | OIDC/JWKS、workload/device cert、CSRF/WS ticket、key rotation | unknown kid/expired/replay fail closed；撤销驱逐 session | compromised key≤15m 阻断；JWKS max-stale≤15m；双 key overlap≤24h；ticket≤30s 且 replay=0 | token/cert timeline、audit / Security |
| DEV-IDEM-001 | A simulator，M 每 adapter | 10k commands；side effect 后断电、ACK 丢失、WAL 满/损坏 | 重启返回原结果或 UNKNOWN；不同 hash 拒绝；危险动作不盲重试 | 支持幂等 adapter 重复副作用=0 | execution journal、物理/外部计数 / Device |
| DR-REGION-001 | A lab weekly，M quarterly | home/DR 分区、双重提升、仲裁不可用、旧区复网、failback | 唯一 active lease；epoch 单调；无 permit 时 fail closed；可由 journal/receipt/旧区证据发现的缺失命令 UNKNOWN，不可发现记录计 RPO | RTO≤60m、RPO≤1m；双区控制写=0 | arbitrator log、DB diff、device journals / Reliability |
| OPS-BACKUP-001 | A weekly，M quarterly | 隔离账号从 base backup+WAL 恢复，再用空 Redis 启动 | schema/约束/tenant/event/audit 一致，真实命令 E2E 成功 | logical RTO≤4h、PITR gap≤15m | backup IDs、restore logs、E2E / Data |
| LOAD-N1-001 | A RC，绑定候选 image digest | 第16节 envelope；失一 AZ、hot tenant、5× burst；GA RC 24h soak | 其他 tenant SLO 保持；最后12h每5m资源采样稳健斜率95% CI含0 | 配额≤3次 N-1 最小 break point 的65%；资源终值≤硬上限70%；慢 WS 影响≤20% | raw histograms、pprof、FD/WAL/network / Performance |
| REL-CANARY-001 | A RC | 生产同型预生产中演练 1/5/25/50/100% controller 阶段、7日合成 baseline、注入回归并回滚 | stop rule 必须触发；schema N/N-1；outbox/连接/命令恢复 | rollback 从首次 stop condition 起≤15m；GW 95%≤15s/99%≤30s | controller decisions、metrics、rollback E2E / Release |
| PROD-CANARY-001 | C 每发布 | 真实 production canary 1/5/25/50/100% 与24h观察；同类生产7日 baseline | 当前 cohort 满足正式 SLO/安全不变量；每级单独批准 | 任何 stop rule 自动回滚；stage 阈值见第17.3节 | stage decisions、production run_id、final closure / Release |
| REL-MIXED-001 | A RC | v1/v2 Gateway 与 Publisher N/N-1 混版、旧 PEL、drain 和 rollback | 只向兼容 instance 发布；同一命令不经两种 envelope 执行；旧 PEL 有界退休 | drain 后5m内旧 PEL=0；错误执行/重复副作用=0 | capability registry、stream/PEL、device journal / Edge |
| SUP-CHAIN-001 | A 每发布 | 提交未签名、错误 provenance、高危镜像、secret、禁用许可证 | CI/admission 必须拒绝；生产只接受批准 digest | 绕过=0 | SBOM、signature、provenance、policy log / Security |
| GOV-DELETE-001 | A RC，M quarterly | tenant export/delete、legal hold、backup expiry | active/hot/archive/backup 按政策处理；他租户不受影响 | SLA 由 ADR-011 冻结；越权/残留超窗=0 | manifest、deletion proof、hold audit / Data |
| AUDIT-001 | A RC，M quarterly | audit DB write fail、WORM export fail、历史篡改 | mutation fail closed；export 重试；篡改被发现 | 未审计 mutation=0；MTTD≤5m | tx result、WORM manifest、alert / Security |
| OPS-KILL-001 | M RC/quarterly | kill switch、lease revoke、quarantine、read-only、暂停 claim、UNKNOWN 人工 reconcile、双人审批 | 每个控制可恢复且全量审计；禁止越权旁路 | 启动≤5m；解除后 E2E 通过 | operator timeline、approvals、audit / Incident |
| OBS-001 | A RC，C production | 阻塞/丢弃 OTel、日志和指标管道；触发每个关键告警 | 观测故障不反压控制链；合成探针能发现用户影响 | paging MTTD≤5m；控制 SLO 不越阈 | alert notification、trace/log/metric links / Reliability |

## Go/No-Go 汇总

Go/No-Go 分三个决策点，不能用尚未获准进入的生产阶段作为前置条件：

1. Pre-canary Go：所有 digest-bound A 场景（包括 LOAD-N1-001、REL-CANARY-001）在候选 digest 上通过；DR/backup 的基础设施恢复步骤可复用 7 天内证据，但恢复后的应用不变量、空 Redis 重建和 E2E 必须在候选 digest 上重跑。任何 application/build-dependency、schema、IaC、云服务主版本或 policy hash 变化都会使相关证据失效。
2. Stage promotion Go：PROD-CANARY-001 的当前 cohort 已达到最小时长/样本、正式 SLO 与安全门禁，没有 stop condition，Release 与 SRE 对本级签字后才进入下一级。
3. Final release closure：100% 流量后 24 小时观察通过，所有 C 探针连续健康，归档 final run_id；失败则回滚并保持发布未关闭。

三个决策点共同要求：到期的 M 场景有当前周期签字；C 探针在首次 canary 前预热至少 24 小时；没有任何未关闭 P0/P1；过期、无四方审批或依赖证据已失效的 waiver 一律 No-Go；失败 run_id 不覆盖且与修复运行关联；Security、Data/Privacy、Reliability/SRE、Device/Adapter 分别签字。
