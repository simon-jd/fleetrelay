# FleetRelay 生产设计基线

> 从已验证的本地控制平面原型演进到可上线、可审计、可恢复的多租户设备控制服务

- 状态：Proposed；作为生产实现和评审基线，当前代码尚未满足本文门禁
- 版本：1.0
- 日期：2026-09-01
- 初始部署模型：单 home region、多可用区、跨区域 warm standby
- 正确性语义：at-least-once delivery + 设备端持久幂等；不声明 exactly-once
- 适用范围：共享 SaaS；单租户部署仍保留 tenant-aware schema 和身份边界

本文补充 [FleetRelay V1 设计规格](../2026-08-31-fleetrelay-v1-design.md)。V1 文档描述产品命题和原型目标；本文定义生产架构、上线门禁和事故恢复规则。两者冲突时，生产部署以本文为准。当前实现证据见 [原型验证报告](evidence/validation-report.md)。

## 1. 生产目标与边界

FleetRelay 的生产目标不是让所有依赖永不失败，而是在浏览器、服务、Redis、Gateway、数据库和设备连接均可能失败时，仍保持以下事实：

1. 未授权、超过签名 dispatch permit 截止时间、已观察到更高 lease/session/region fence 的命令不能开始设备副作用；租约撤销遵守第 7.3 节定义的有界生效窗口。
2. 已被 API 确认接受的控制命令在 home region 内不丢失。
3. 系统不把未知执行结果伪装为成功或未执行。
4. Redis 全量丢失只影响实时性，不改变租约、命令、attempt、事件和审计事实。
5. 浏览器断线恢复可以证明持久事件连续，不依赖“当时恰好在线”。
6. 支持重试的设备在进程重启后仍能识别同一 command_id。
7. 任一租户不能通过 ID、cursor、Redis topic、缓存键或资源耗尽影响其他租户的安全边界。

初始生产版本不包含：

- 多区域 active-active 控制写入；
- 端到端 exactly-once；
- 任意厂商机器人协议逆向；
- 视频、音频或高带宽媒体数据面；
- Service Mesh、自研 Kubernetes Operator 或第二套消息平台；
- 未通过实测的性能、真实硬件或灾备声明。

## 2. 当前基线与上线差距

| 能力 | 当前原型 | 生产目标 | 上线级别 |
|---|---|---|---|
| 权威状态 | PostgreSQL 租约、命令、attempt、audit、outbox | tenant-aware schema、正式迁移、HA、PITR、不可变审计 | P0 |
| 命令投递 | PG outbox、Redis Stream、有限重试和晚 ACK | attempt 与 delivery outbox 同事务、工作项租约、poison 隔离、对账 | P0 |
| 浏览器事件 | snapshot、当前状态轮询、零事件 resume | tenant offset、durable replay、无缝 replay/live 切换 | P0 |
| 设备幂等 | 模拟器进程内缓存 | 设备 SDK/Bridge 持久执行日志、重启恢复、UNKNOWN 对账 | P0 |
| 身份 | 静态开发 token、明文设备 gRPC | OIDC、workload mTLS、设备 PKI、managed secrets | P0 |
| 租户隔离 | 无 tenant_id | tenant-scoped PK/FK/unique、RLS、配额、公平调度 | P0 |
| 区域隔离 | 无 region epoch | home region + control_plane_epoch + warm standby | P0 |
| 可观测性 | health、slog、静态 dashboard 脚手架 | 全链路 OTel、业务 SLI、burn-rate 告警、合成探针 | P1 |
| 部署 | Docker Compose | 三 AZ、N+1、drain、canary、不可变制品、自动回滚 | P1 |
| 验证 | 本地独立 E2E 和故障测试 | CI integration、nightly HA、RC chaos、soak、季度 DR | P1 |
| Vacuum Bridge | 仅 simulator | VendorDriver contract、持久幂等、真实硬件证据 | P2 |

P0 是任何公网或真实设备控制上线前的硬阻断项。P1 必须在正式 GA 前完成；只有隔离的内部试点可以在风险签字后使用部分 P1 能力。P2 只阻断相应厂商硬件，不阻断通用控制平面。

## 3. 不可破坏的生产不变量

- PostgreSQL 是租约、命令、attempt、投递意图、持久事件和审计的唯一权威。
- Redis、Gateway 内存和浏览器缓存全部可删除、可重建，不能授权或决定终态。
- 每次权威状态变更、audit、domain event 和 event publication intent 必须在同一 PostgreSQL 事务提交；每个 attempt 与其 delivery_outbox 也必须在同一事务提交。
- idempotency key 与规范化请求摘要绑定；同 key 不同语义返回 CONFLICT。
- 每个 tenant/device 的 device_sequence 单调递增，普通控制命令至多一个处于不确定执行状态。
- lease_epoch、session_generation、gateway_instance 和 control_plane_epoch 是不同层级的 fencing token，不能合并。
- 重试复用原 command_id，只新增 attempt 和 delivery_id。
- ACK_TIMEOUT 的业务语义是 UNKNOWN_OUTCOME，不是“设备未执行”。
- 所有授权、租约和截止时间使用 PostgreSQL 权威时间；应用本地时钟只用于超时等待和遥测。
- REQUIRE_ONLINE 无法验证完整 tenant/device/session/Gateway instance 时必须 fail closed。
- 浏览器可靠控制事件和可合并遥测使用不同队列、保留和背压策略。
- 设备或 VendorDriver 不具备持久幂等/对账能力时，不自动重试可能产生不可逆副作用的命令。

## 4. 命令安全分类

每个 capability 必须在设备注册策略中声明安全分类，服务端不能从命令名字猜测。

| 分类 | 示例 | 模糊结果后的行为 |
|---|---|---|
| QUERY_ONLY | QUERY_STATE | 可安全重试 |
| REPLAY_SAFE | 设置目标温度、设置模式 | 使用同 command_id 自动重试 |
| RECONCILABLE | START_TASK、RETURN_HOME | 先查询设备状态；确认未生效后才重试 |
| NON_RETRYABLE | 厂商不支持 operation token 的一次性动作 | 进入 UNKNOWN，禁止自动重试 |
| SAFETY_CRITICAL | 开锁、移动重型设备、急停复位 | step-up/双人审批；默认不自动重试 |

UNKNOWN_OUTCOME 会阻塞同设备后续普通控制命令。解除方式只有：

1. 有效迟到 ACK；
2. VendorDriver 状态查询给出可证明结果；
3. 经过审计的人工 reconcile；
4. 策略明确允许的 QUERY_ONLY 命令。

## 5. 生产参考拓扑

~~~mermaid
flowchart TB
    Users["Operator browsers"] --> Edge["DNS / WAF / L7 LB"]
    Devices["Devices / Vacuum Bridge"] --> DeviceLB["Device L4/L7 LB"]
    Edge --> OGW["Operator Gateway pool (3+ AZ)"]
    DeviceLB --> DGW["Device Gateway pool (3+ AZ)"]
    OGW --> API["Control API pool (3+ AZ)"]
    DGW --> API
    DGW <--> Redis["Redis HA / TLS / ephemeral"]
    OGW <--> Redis
    Dispatch["Dispatcher workers (3+)"] <--> Redis
    Dispatch --> PG["Managed PostgreSQL HA"]
    API --> PG
    Projector["Event projector / replay workers"] --> PG
    Projector --> Redis
    PG --> Archive["Encrypted backup / WORM audit archive"]
    IdP["OIDC IdP"] --> Edge
    PKI["Device and workload PKI"] --> DGW
    KMS["KMS / Secrets Manager"] --> API
    KMS --> Dispatch
    KMS --> OGW
    KMS --> DGW
    API --> OTel["OTel pipeline"]
    Dispatch --> OTel
    OGW --> OTel
    DGW --> OTel
~~~

仍保留 control-api、stream-gateway、dispatcher 三个代码和业务边界。为隔离长连接资源，同一个 stream-gateway 镜像使用不同配置部署为 Device Gateway pool 和 Operator Gateway pool；这不是新增微服务。

### 5.1 最小副本与容量规则

| 工作负载 | 最小副本 | 扩缩容主要信号 | 生产约束 |
|---|---:|---|---|
| Control API | 3 | 请求率、事务延迟、PG pool 等待 | 跨 3 AZ；无本地权威状态 |
| Device Gateway | 3 | 活跃流、FD、发送队列、重连率 | N+1；连接感知 drain |
| Operator Gateway | 3 | WS 数、订阅数、可靠队列字节数 | 慢客户端隔离 |
| Dispatcher | 3 | outbox age、claim lag、attempt backlog | DB 工作项租约和 term fencing |
| Event projector | 2+ | event publication lag、replay QPS | 幂等发布；可水平 claim |

参考生产平台采用托管 Kubernetes，跨三个可用区部署，不引入 Service Mesh。也可映射到具备等价反亲和、身份、drain、网络策略和不可变部署能力的托管容器平台。

外部 LB 必须支持 gRPC/HTTP2 与 WebSocket，idle timeout 大于三个心跳周期；客户端重连使用指数退避、随机 jitter 和服务器 Retry-After。Gateway 发布先撤销 readiness，停止接收新连接，标记 DRAINING，发送 DisconnectNotice，等待有界时间后关闭。

## 6. 多租户身份与数据隔离

生产 schema 中所有业务表必须包含不可空 tenant_id。身份不是单独的 device_id，而是 tenant_id/device_id；任何跨边界缓存和路由都使用完整身份。

### 6.1 数据库约束

- 主键、外键和唯一约束全部 tenant-scoped。
- devices：唯一键为 tenant_id, device_id。
- commands：唯一键包括 tenant_id, operator_id, device_id, idempotency_key。
- 每设备顺序：tenant_id, device_id, device_sequence 唯一。
- attempts、events、audit、outbox 的 FK 均包含 tenant_id，禁止只按 UUID 猜租户。
- PostgreSQL RLS 作为纵深防御；每个事务显式 SET LOCAL app.tenant_id。
- 应用数据库角色不是表 owner、superuser 或 BYPASSRLS。
- 运维/迁移角色独立，使用短期凭据并写入安全审计。

### 6.2 Redis 与内存隔离

- presence key：tenant/{tenant}/device/{device}/presence。
- Gateway stream：tenant shard + gateway_instance，而不是仅 gateway_id。
- live event stream：tenant shard + event offset。
- Gateway registry key：tenant_id/device_id。
- replay cursor 包含 tenant、filter hash 和 offset，并有服务端签名。
- 每租户限制设备数、连接数、命令率、遥测速率、订阅数、outbox 深度和存储量。
- Dispatcher 使用 tenant-aware weighted fair queue，hot tenant 不能饿死其他租户。

高监管租户可以使用独立 cell、数据库、Redis 和 customer-managed KMS key；隔离级别是正式产品套餐，不在事故中临时决定。

### 6.3 从当前 schema 的迁移顺序

1. 新增可空 tenant_id 和 legacy tenant，应用双写。
2. 后台分批回填，记录进度和校验 hash。
3. 新增 tenant-scoped 索引、唯一约束和复合 FK，使用低锁迁移。
4. 新旧版本双读验证，负向跨租户测试进入 CI。
5. tenant_id 改为 NOT NULL，启用并强制 RLS。
6. 删除旧全局约束和兼容路径。

迁移期间旧版本最多兼容一个相邻版本；禁止在同一发布中直接重写大表并删除旧列。

## 7. 权威数据模型与事务

### 7.1 生产新增或强化的表

| 表 | 核心字段与目的 |
|---|---|
| tenants | 状态、region、配额、数据保留策略 |
| devices | capability policy、adapter、证书主体、状态 |
| device_sessions | gateway_instance、session_generation、control_plane_epoch、开始/结束原因 |
| control_leases | lease_epoch、server-issued expiry、owner、撤销原因 |
| commands | request_hash、安全分类、sequence、region epoch、状态、reconcile 状态 |
| command_work | 待路由/待对账工作、next_action_at、owner、lease、scheduler term |
| command_attempts | attempt、目标 instance/session、不可变 envelope_version/capability_generation、route/ACK deadline、scheduler_term |
| delivery_outbox | 与单个 attempt 绑定的 delivery_id、不可变 envelope_version、READY/LEASED/PUBLISHED/DEAD、owner、lease、last_error |
| dispatch_authorizations | permit nonce、签名摘要、not_after、ISSUED/CONSUMED/EXPIRED、设备 receipt；attempt 单独记录 possibly_sent_at |
| domain_events | tenant offset、event_id、schema version、aggregate、payload、occurred_at |
| tenant_event_heads | 每租户事务性 next_offset |
| event_publications | 与 domain event 绑定的幂等 live publication 状态；不影响 replay 权威性 |
| audit_records | actor、decision、resource、result、trace、不可变 payload 摘要 |
| scheduler_partitions | partition、owner、term、lease_until，用于旧 leader fencing |

### 7.2 命令接受事务

Control API 在 SERIALIZABLE 或等价显式锁事务中：

1. 从数据库获取当前时间。
2. 验证 tenant、actor、RBAC/ABAC、capability、租约和配额。
3. 规范化请求并计算 request_hash。
4. 查找 idempotency key：摘要相同返回原 command；不同返回 CONFLICT。
5. 锁定每设备顺序行，分配 device_sequence。
6. 插入 command。
7. 插入 command.created domain event、event_publication intent 和 audit。
8. 插入 command_work，要求 Scheduler 在设备可路由时创建 attempt；此时没有 attempt 或 Redis 事实。
9. 提交后才向客户端返回 ACCEPTED。

客户端遇到 ambiguous commit 必须使用原 idempotency key 查询或重试，不能生成新 key。

### 7.3 投递 claim 与 attempt

生产 Dispatcher 内部分为 command scheduler、delivery publisher 和 deadline reconciler，不依赖单个进程的 session advisory lock 保证正确性：

- Scheduler 用 FOR UPDATE SKIP LOCKED claim 到期的 command_work，在同一事务中重新验证命令、租约、session、gateway instance、region epoch 和设备顺序，锁定目标 instance 的 capability generation，然后同时插入 command_attempt 和与它绑定的 delivery_outbox。envelope_version 与 capability_generation 在此时持久化且不可变。
- Delivery publisher 独立 claim READY 或 lease 过期的 delivery_outbox。Redis XADD 使用稳定 delivery_id；发布重试复用同一个 attempt 和 delivery_id，不消耗新的 attempt budget。
- Redis Stream 只携带不可信的 tenant_id、opaque delivery_id，不携带可直接执行的命令 payload。Gateway 收到后通过 workload mTLS 调用 Control API GetDelivery(tenant_id, delivery_id, gateway_instance)。Control API 先以 tenant_id 建立 RLS 事务上下文，再通过 tenant-scoped delivery FK 重新验证命令 hash、租约、region lease、session、instance、sequence 和 expiry；跨租户同 delivery_id、错误 stream shard 或错误 Gateway 一律拒绝。Redis 或 publisher 凭据泄漏最多造成 DoS，不能伪造可执行命令。
- XADD 成功后将 delivery_outbox 标记 PUBLISHED；提交与 XADD 间崩溃只会导致同一 delivery_id 重复发布。PUBLISHED 只表示进入 Gateway transport，不表示已写入设备流。
- dispatch permit 是 Control API 工作负载密钥签名的一次性对象，绑定 tenant/device/incarnation、command fingerprint、delivery/attempt、session/gateway、lease/region epoch 和 nonce；`not_after = min(command expires_at, control lease expires_at, active-region lease_until, route_deadline, issued_at + 2s)`。Gateway 在实际写入前复验签名、绑定值和 not_after；设备用认证握手获得的 server-time anchor 加本地安全单调时钟再次复验，并拒绝已观察到更高 fence 的 permit。无法提供可信时间或持久最高 fence 的设备对写控制 fail closed。
- Gateway 成功写入 gRPC 流后回报 delivery_id、permit nonce 和实际 session；Control API 原子记录 dispatched_at 并从该时刻计算 ACK deadline。route deadline 与 ACK deadline 分开，路由失败不伪装成设备执行超时。租约撤销事务立即提升 lease_epoch 并停止签发 permit，向在线设备发送 signed fence；撤销对新物理动作的最大生效窗口为 2 秒，只有设备确认 fence 或全部旧 permit 到期后才能向操作员报告 REVOKED_EFFECTIVE。已经在有效 permit 下开始的物理动作只能取消或对账，不能宣称被瞬时撤销。
- GetDelivery 在返回 permit 前以同一事务提交 ISSUED dispatch_authorization，并把 attempt 从 CREATED 转为 POSSIBLY_SENT；这个保守边界发生在响应写出之前，因此崩溃最多产生“未实际发送但按可能发送处理”，不会产生“已发送却仍按未发送处理”。相同 delivery/gateway/session 的重入在 not_after 前返回同一 nonce 和同一签名摘要，不生成第二个授权；过期后只有安全分类允许时才能签发新 nonce。
- 每个 delivery 通过 partial unique index 保证最多一个 state=ISSUED 的 authorization；GetDelivery 先锁 attempt，并用 compare-and-swap 创建或返回现有 nonce。MarkDispatched 在一个事务执行 authorization ISSUED→CONSUMED 与 attempt POSSIBLY_SENT→DISPATCHED；fast ACK 在一个事务完成 authorization CONSUMED、attempt ACKED、command 终态、audit 和 event。
- Gateway 仅在 Control API 已记录 DISPATCHED，或已按安全分类持久化为“旧 attempt SUPERSEDED + 新 ROUTE work”、RECONCILING 或 UNKNOWN_OUTCOME 后 XACK Redis entry。Gateway 崩溃留下的 PEL 可重领，但不能靠 PEL 是否存在推断设备未执行。
- Deadline reconciler 只有在旧 attempt 被确认 superseded/route-timeout，或命令策略允许从 UNKNOWN 再投递时，才创建下一条 command_work。
- 超过发布重试上限或不可解析消息进入 DEAD，并产生告警和 audit，不能成为永久 poison row。
- next_action_at 使用指数退避、jitter 和按 tenant 公平调度。
- Gateway Stream 按 gateway_instance 隔离；实例退役后清理 consumer、PEL 和已确认 entry，并以 age/size 上限防止无人认领的 Stream 无界增长。

scheduler_partitions 的 term 会写入 attempt。初始 route、deadline、reconcile 的写入都必须验证当前 term，旧 leader 即使网络恢复也不能继续修改状态。

### 7.4 ACK 与对账

ACK 事务同时校验：

- tenant_id、device_id、command_id；
- control_plane_epoch、lease_epoch、device_sequence；
- gateway_instance、session_generation；
- attempt 具有 was_dispatched=true，或具有 was_possibly_sent=true 加可验证历史 ISSUED/CONSUMED/EXPIRED authorization；当前 status 可以是 POSSIBLY_SENT、DISPATCHED、SUPERSEDED 或 UNKNOWN，ACK 可以来自非最新 attempt，但不能来自从未签发 permit 的 delivery；
- 结果与当前状态机是否兼容。

新的投递只能面向最新 session；ACK eligibility 由不可变证据列判断，不由当前 attempt status 判断。旧 session 的 SUPERSEDED/UNKNOWN attempt 只要保留 was_dispatched，或 was_possibly_sent 加可验证历史 permit，也能提供迟到执行证据；设备 mTLS 身份、permit 签名、delivery 和全部 epoch/sequence 仍必须匹配。authorization 的 EXPIRED 只禁止再次发送，不抹掉历史执行证据。ACK 事务把未消费 authorization 置 CONSUMED、补记 dispatched_at/设备 receipt 并落终态；已 CONSUMED/EXPIRED 行保持历史状态并关联 receipt。状态、结果、audit、domain event 和 event_publication intent 在一个事务提交。实际 Redis publication 永远发生在事务后；PUBLISHED 只表示实时投影进度，不是业务事实。重复 ACK 返回原结果；冲突终态进入安全告警。有效迟到 ACK 可以从 UNKNOWN_OUTCOME 对账为 SUCCEEDED 或 FAILED，但不能覆盖另一个已有终态。

### 7.5 command_work 状态与 claim

work kind 固定为 ROUTE、RECONCILE、QUERY_STATE。人工复核是 command 的 reconcile 状态，不由后台 worker 假装完成人工决策。

~~~text
READY -> LEASED -> DONE
   |         |       |
   |         +------> READY        retryable/reschedule
   +----------------> CANCELLED    command terminal/lease invalid
   +----------------> DEAD         poison or bounded permanent failure
~~~

约束与算法：

- 唯一键为 tenant_id, command_id, work_kind, generation；partial unique 保证一个 command 最多一个 active ROUTE 和一个 active RECONCILE。
- claim 和 lease 使用 PostgreSQL 时间。事务内 SELECT ... FOR UPDATE SKIP LOCKED，随后写 owner、lease_token、partition_term、lease_until 和 state=LEASED。
- 行锁只隔离 claim 事务；持久 lease_token 负责 worker 在事务提交后崩溃或失联时的回收。
- worker 的 attempt/outbox 插入、reschedule、DONE、CANCELLED、DEAD 都使用条件写：owner、lease_token、partition_term 匹配且数据库时间未超过 lease_until。
- worker 丢失 lease 后不得提交任何结果。lease 过期行由其他 worker 使用新 token 回收。
- Scheduler 成功原子创建 attempt + delivery_outbox 时把 ROUTE work 置 DONE。暂时离线或依赖不可用时清空 lease、增加 retry_count、计算 next_action_at 并回到 READY。
- stale lease 在路由前把 command 转 CANCELLED；command expiry 转 EXPIRED；未被 API 接受的请求才使用 REJECTED。
- 前序普通命令终态事件会唤醒下一条被顺序阻塞的 work；周期 sweep 是漏唤醒的安全网，不是主要触发机制。

scheduler_partitions 通过单事务在 lease 已过期时递增 term 并换 owner。初始 ROUTE、deadline、reconcile 创建 attempt 的事务都必须锁定 partition row，并验证 owner、term 和 DB-time lease。Delivery publisher 的 PUBLISHED/DEAD 更新也必须验证自己的 claim generation 和 lease_token，旧 owner 不能覆盖新 owner。

### 7.6 attempt 与 delivery 状态

~~~text
attempt:  CREATED -> POSSIBLY_SENT -> DISPATCHED -> ACKED
             |            |              |
             +-> ROUTE_TIMEOUT           +-> UNKNOWN
             +-> SUPERSEDED +-> UNKNOWN  +-> SUPERSEDED

delivery: READY -> LEASED -> PUBLISHED -> RETIRED
             |         |          |
             +-> READY +-> DEAD   +-> READY  (same delivery_id republish)
~~~

- UNIQUE(tenant_id, command_id, attempt_number)；UNIQUE(tenant_id, delivery_id)。delivery_id 本身使用 128-bit 随机值，但安全边界仍由 tenant-scoped FK 和 RLS 提供。
- command_attempts.status 使用 CREATED、POSSIBLY_SENT、DISPATCHED、ACKED、ROUTE_TIMEOUT、UNKNOWN、SUPERSEDED；delivery_outbox.status 独立使用 READY、LEASED、PUBLISHED、RETIRED、DEAD。前者描述设备执行证据，后者只描述 transport publication，不能互相覆盖。
- 一个 attempt 严格绑定一个 delivery_id。Redis publication 重试只增加 publish_count，不创建新 attempt。
- 同一 delivery 的所有 publication 必须复用持久 envelope_version 和 capability_generation；Gateway 能力变化或 v1/v2 切换不能原地改格式。必须换格式时创建新 attempt/delivery，并先执行第 7.8 节的安全分类决策。
- Gateway 的权威 envelope 获取、MarkDispatched 和 ACK 都携带 delivery_id、attempt_number、gateway_instance 和 session_generation。
- PUBLISHED 只说明 delivery 可能在 Redis；DISPATCHED 必须由 Gateway 成功写入设备 gRPC 流后的条件事务产生。
- route_deadline 管 Redis/Gateway 路由；ack_deadline 从 DISPATCHED 提交时开始。二者使用独立预算和指标。
- publication DEAD 不计入设备执行重试次数，但必须进入 transport 告警和安全分类决策；DISPATCHED attempt 才计 execution_attempt_count。
- SUPERSEDED/UNKNOWN 不能抹掉 was_dispatched/was_possibly_sent 和 authorization 关联。任何 was_dispatched=true，或 was_possibly_sent=true 加历史 authorization，且 fencing/hash 匹配的 attempt 都可提供迟到 ACK 证据。
- 多 attempt 返回不同终态时，command 进入 RESULT_CONFLICT，冻结该设备普通命令并触发安全人工复核。
- permit 已签发后的任何模糊窗口按 POSSIBLY_SENT 处理：QUERY_ONLY 和已证明 durable-idempotency-v1 的 REPLAY_SAFE 可复用 command_id 安全重试；RECONCILABLE 先查询；NON_RETRYABLE 与 SAFETY_CRITICAL 进入 UNKNOWN_OUTCOME，禁止统一重发。

### 7.7 事务锁顺序与重试

所有会同时触碰调度和领域状态的事务使用统一锁顺序：scheduler_partition → command_work → device/command → command_attempt/dispatch_authorization/delivery_outbox → tenant_event_head。只需要后段对象的事务可以从该对象开始，但不得再反向获取前段锁。SERIALIZABLE serialization failure、deadlock detected 和连接 ambiguous commit 使用有界 jitter 重试；重试始终复用 idempotency key、work lease_token 或 delivery_id，超过预算进入显式可观测错误，不绕过锁顺序。

### 7.8 安全分类转换矩阵

| 失败点 | QUERY_ONLY | REPLAY_SAFE | RECONCILABLE | NON_RETRYABLE | SAFETY_CRITICAL |
|---|---|---|---|---|---|
| 路由前 lease 失效 | CANCELLED | CANCELLED | CANCELLED | CANCELLED | CANCELLED |
| 命令已过 expires_at | EXPIRED | EXPIRED | EXPIRED | EXPIRED | EXPIRED |
| 未确认写入设备流 | 重用 command_id 路由 | 仅设备声明 durable-idempotency-v1 时重路由 | 先 QUERY_STATE | UNKNOWN | UNKNOWN + 人工审批 |
| DISPATCHED 无 ACK | 自动重试 | 仅 durable-idempotency-v1 设备有界自动重试 | RECONCILING + QUERY_STATE | UNKNOWN | UNKNOWN + 双人复核 |
| publisher DEAD/ambiguous XADD | 同 delivery_id 重发 | 同 delivery_id 重发 | 同 delivery_id 重发；超窗后查询 | UNKNOWN | UNKNOWN |
| 冲突 ACK | RESULT_CONFLICT | RESULT_CONFLICT | RESULT_CONFLICT | RESULT_CONFLICT | RESULT_CONFLICT + 紧急告警 |

生产数据库使用 UNKNOWN_OUTCOME；旧 ACK_TIMEOUT API 值在兼容窗口内映射到 UNKNOWN_OUTCOME 并标记 legacy_reason=ACK_TIMEOUT。QUERY_STATE work 是只读控制查询，可按 capability 策略越过被 UNKNOWN 阻塞的普通写命令，但不能改变设备物理状态。

## 8. 持久事件、WebSocket 回放与 fan-out

每个 domain_event 在同一业务事务插入一条稳定 event_publication intent。其状态为 READY、LEASED、PUBLISHED 或 DEAD，并包含 owner、lease_token、lease_until、retry_count、next_attempt_at 和 last_error。Projector 以 DB time 和条件更新 claim；实际 Redis XADD 在事务外执行，使用稳定 event_id 幂等。PUBLISHED 不是领域事实，publication DEAD 也不影响 PostgreSQL replay，但必须告警。

Redis 集群 epoch 变化或全量重建时，Projector 可以把 hot-retention 内的 event_publications 重置为 READY，或从 domain_events 按 offset 重建 publication intent。重复 live event 由 event_id/offset 去重。

### 8.1 事件 offset

每个 tenant 使用 tenant_event_heads 行分配严格单调、事务性的 offset。更新业务状态的同一事务锁住该行、递增 offset 并写 domain_events。事务回滚时 offset 也回滚，因此浏览器不会遇到由未提交事务造成的永久 cursor 空洞。

高吞吐 tenant 超过单 offset 行实测上限后，迁移到显式 event shard 和 vector cursor；在出现容量证据前不引入 Kafka。

### 8.2 Cursor

cursor 是不透明、版本化、带签名的 token，包含：

- tenant_id；
- last_offset；
- subscription filter hash；
- schema version；
- issued_at 和最大有效期。

filter 使用 canonical form：topic 排序、device 集合排序去重、授权 scope/version 固定编码后计算 hash。cursor signing key 支持 key id 和双密钥轮换。服务端不信任客户端提供的 tenant 或 filter；授权范围或 policy version 变化会使旧 cursor 失效并要求 resnapshot。cursor 过旧且事件已超出 hot retention 时返回 410 RESNAPSHOT_REQUIRED。

### 8.3 无缝 replay/live 算法

1. Operator Gateway 完成身份和订阅授权，并开始读取完整 tenant offset 序列，在有界内存中暂存 live 事件。
2. 初次订阅在同一 PostgreSQL REPEATABLE READ 事务以读取 tenant_event_head 作为建立 MVCC snapshot 的首个业务查询，得到 H 后读取授权 snapshot 并签发 cursor(H)。状态变化与 domain event 同事务，因此在该 MVCC view 不会出现只有状态或只有 event 的提交。恢复订阅读取 cursor C 后，以同样顺序在一致性视图固定 H。
3. 初次订阅发送 snapshot(H)；恢复订阅从 PostgreSQL 读取 C+1..H。Gateway 内部处理完整 tenant 序列，只有通过当前对象授权和 filter 的事件才发给客户端。
4. Projector 对同一 tenant 串行发布 offset。Gateway 仍按 offset 排序、对 event_id 去重，绝不在确认 H+1 之前发送 H+2。
5. 对 buffer 中每个缺口先从 PostgreSQL 查询并确认是授权后发送、被 filter 跳过，还是尚未到达；处理完后再推进内部 offset。
6. 重读 durable watermark 并重复 catch-up，直到数据库、buffer 和 live head 追平，再切换到 live。
7. buffer 溢出、授权变化或无法在期限内追平时丢弃本次 handoff，返回 1013/RESNAPSHOT_REQUIRED 并从数据库重新开始，不能静默丢事件。

Redis 中断或 Gateway 重启不影响 replay 正确性。其他浏览器也能收到状态变化，不再为每条命令启动 200 ms 数据库轮询。

Redis live stream 按 tenant shard 保留短窗口。每个 Operator Gateway instance 使用独立读取位置或独立 consumer group，保证所有承载相关订阅的 Gateway 都看到事件；不能把多个 Gateway 放进一个会分摊事件的共享 consumer group。退役实例的 group 和 PEL 由 janitor 清理。

tenant-global offset 不直接暴露给低权限客户端。客户端 cursor 加密并带随机 nonce，无法观察数值跳跃。Gateway 对被 filter 或对象授权排除的事件只推进内部 offset；可见事件携带新的 opaque cursor。固定周期 heartbeat 可携带随机化 checkpoint，因此不会因隐藏事件发生才额外发包。

### 8.4 背压

- 每条 WS 连接同时限制可靠事件数量和总字节数，初始上限由负载测试确定。
- telemetry 使用每 tenant/device/topic 最新值合并，不占用可靠事件队列。
- 可靠队列超过高水位时暂停订阅并关闭连接，返回 1013 和最后已发送 cursor。
- 客户端持久保存最后完整处理的 cursor；重连从该 cursor replay。
- server 可接受 event.ack 统计消费进度，但正确性不依赖服务端保存浏览器 cursor。

## 9. 设备身份、幂等和 Vendor Bridge

### 9.1 设备 PKI

- 每台设备使用唯一、硬件保护的私钥。
- mTLS SAN 绑定 tenant 和 device，例如 spiffe://fleetrelay/tenant/T/device/D。
- DeviceHello 必须与证书身份一致；不接受客户端自报的其他 tenant/device。
- enrollment 使用短期一次性 token、设备证明和审批策略。
- 证书短生命周期并在有效期三分之二前轮换；支持双证书重叠。
- 吊销会触发 quarantine、现有 session 驱逐、租约撤销和告警。

### 9.2 持久执行日志

设备 SDK 或 Vacuum Bridge 使用 SQLite、BoltDB 或等价 WAL 保存：

- command_id 和规范化 command hash；
- control_plane_epoch、lease_epoch、device_sequence；
- RECEIVED、EXECUTING、COMPLETED、FAILED、UNKNOWN；
- 可重放的结果和 adapter operation token；
- 写入和最后访问时间。

执行流程：

1. 验证 fencing、expiry、capability 和请求 hash。
2. 在本地事务持久化 RECEIVED 后才执行。
3. 已有 COMPLETED/FAILED 记录直接返回缓存结果。
4. 持久化 EXECUTING，再调用 VendorDriver。
5. 持久化最终结果并 fsync，再发送 ACK。
6. 重启发现 EXECUTING 时进入 UNKNOWN：先查询厂商状态或 operation token；不得盲目重放 NON_RETRYABLE/SAFETY_CRITICAL 动作。

日志保留期必须大于服务端最大命令重放和审计窗口。存储满时设备停止接受新控制命令并上报明确告警，不能降级为无幂等执行。

设备每次建立新 session 时还会上报最高 control_plane_epoch、last device_sequence 和最近执行日志摘要。Control API 只把它用于对账，不允许设备反向提升授权；服务端缺失但设备已执行的区域故障记录进入人工或策略化 reconcile。

### 9.3 Device SDK 协议细节

- Command Fingerprint v1 使用 SHA-256，输入包括 fingerprint version、tenant/device/incarnation、command_id、command type、RFC 8785 规范化 payload、control_plane_epoch、lease_epoch、device_sequence 和 expires_at；排除 attempt、delivery、gateway 和 session 等传输字段。仓库保存跨 Go/设备语言的 golden vectors。
- 设备收到相同 command_id 但 fingerprint 不同时立即拒绝、quarantine 当前 session 并产生安全事件。
- adapter operation token 在执行前由 command_id/device incarnation 确定性派生或由服务端提供，先和 RECEIVED 一起 fsync，再传给支持幂等 token 的厂商 API。
- Protobuf 增加 AckResult UNKNOWN、ExecutionQuery、ExecutionReport、delivery_id、attempt_number、command_fingerprint、control_plane_epoch 和 device_incarnation。UNKNOWN、RECONCILING、RESULT_CONFLICT 有明确 wire/API 映射。
- 默认每设备串行执行写命令；只有 adapter capability 明确声明 parallel-query-safe 时，QUERY_ONLY 才能并行。
- ServerHello 下发 signed safe_gc_before。设备只删除早于本地策略和 server watermark 的终态 result；highest control_plane_epoch、device_incarnation、last applied sequence 和拒绝远古重放所需 tombstone 永不随普通 GC 删除。
- WAL 损坏、fsync 失败、磁盘满或安全存储丢失时，设备进入 CONTROL_QUARANTINED，只允许最小健康/诊断遥测，拒绝所有控制命令。
- factory reset 创建新的 device_incarnation 并重新 enrollment；旧证书、session、execution log 和命令不能迁移到新 incarnation。无法读取持久最高 epoch 的设备也必须重新 enrollment。

feature negotiation 不只依赖版本字符串。设备显式声明 durable-idempotency-v1、execution-query-v1、control-plane-epoch-v1 和 delivery-receipt-v1；缺少所需 feature 的设备被限制为 QUERY_ONLY 或隔离试点，不能启用自动控制重试。

### 9.4 Adapter contract

每个 VendorDriver 必须声明 capability、安全分类、厂商 operation token、查询/取消/补偿能力和错误映射。Simulator 与真实 Bridge 运行同一 contract suite；真实硬件证据与模拟器证据分开。

## 10. 身份、授权和安全边界

### 10.1 操作员

- 使用 OIDC Authorization Code + PKCE。
- BFF/Operator Gateway 使用 __Host- 前缀、Secure、HttpOnly、SameSite cookie，禁止 Domain 且 Path=/；不把 token 放入 URL、localStorage 或普通日志。
- REST mutation 要求与 BFF session 绑定的 synchronizer CSRF token header，并校验 Origin/Referer。一次性 WS ticket 最长 30 秒，绑定 session、tenant、Origin、nonce 和目标 Gateway，成功使用即失效，重放被拒绝。
- WebSocket upgrade 验证一次性 WS ticket、Origin、issuer、audience、签名算法、kid、exp、nbf 和会话撤销状态。
- Control API 验证自己的短期 audience token 或 token-exchange delegation；Gateway 不能用共享密钥伪造 actor header。
- 高风险命令要求 step-up MFA 或双人审批，审批本身写 audit。
- JWKS 短暂不可用时，仅使用在策略期限内缓存且仍有效的 key；未知 kid、过期 token 或超过最大 stale window 均 fail closed。

### 10.2 服务身份

- 使用 SPIFFE/SPIRE 或云 workload identity 发放短期 mTLS 身份。
- 每个服务使用独立的最小权限 PostgreSQL、Redis 和 KMS 身份。
- 服务间请求携带可验证 delegation 和 trace，不使用静态 shared token。
- 内部 API 版本化，定义 deadline、幂等和错误码；网络策略只允许声明的调用边。

### 10.3 密钥与加密

- TLS 1.3 覆盖公网和设备入口；所有服务间连接强制 workload mTLS，不允许以“私有网络”替代身份验证。
- Secrets Manager/Vault 保存 secret，KMS envelope encryption 保护数据库、备份和对象存储。
- CI/CD 通过 OIDC 获取短期云凭据，不保存长期生产密钥。
- 轮换支持双密钥窗口并有自动演练；break-glass 有时限、审批和独立审计。

### 10.4 授权

授权维度至少包含 tenant、fleet/site、device group、command capability、设备安全分类和时间条件。RBAC 提供 viewer/operator/admin 基础角色，ABAC 负责资源范围和高风险策略。所有对象读取、cursor replay 和 WebSocket topic 都执行对象级授权。

### 10.5 威胁模型

| 威胁 | 主要控制 |
|---|---|
| 跨租户 IDOR/cursor 泄漏 | tenant-scoped FK/RLS、签名 cursor、负向测试 |
| 设备冒充或证书复制 | 硬件私钥、SAN 绑定、并发 session 检测、快速 quarantine |
| Gateway confused deputy | Control API 自验证 delegation、对象级授权 |
| 命令重放 | request hash、四层 fencing、设备持久执行日志 |
| Redis/publisher 被攻陷 | Stream 只含不可信 tenant_id + opaque delivery_id；Control API 在 tenant RLS 下在线重验并签发短期 dispatch permit |
| 恶意 adapter/payload | capability allowlist、schema 验证、进程隔离、最小文件/网络权限 |
| 遥测洪泛/慢连接 | tenant/device 限流、有界队列、连接配额、优先级隔离 |
| 旧区域复活 | control_plane_epoch、流量隔离、提升流程 |
| 内部管理员滥用 | 最小权限、双人审批、WORM audit、定期访问复核 |
| CA/issuer 或 cursor signing key 失陷 | 双根/双 key 轮换、全局撤销、session/ticket 失效、break-glass 审计 |

## 11. 数据治理

设备位置、家庭遥测、在线时间和命令历史可能推断居住或占用行为，默认按敏感个人数据处理。

初始保留建议，最终需产品、法务和安全批准：

| 数据 | Hot retention | Archive | 删除规则 |
|---|---:|---:|---|
| 原始 telemetry | 7 天 | 默认无；按租户选择 | TTL 自动删除 |
| 聚合 telemetry | 30–90 天 | 可选 | tenant policy |
| command/domain event | 90 天 | 合规需要时加密归档 | 法律保留优先 |
| 应用日志 | 30 天 | 仅事故证据 | 去敏后 TTL |
| 安全 audit | 至少 1 年 | WORM/object lock | 受控法律流程 |
| 备份 | PITR 35 天起 | 跨账户副本 | 过期销毁证明 |

command payload 默认不进入日志、trace 或指标。operator/device ID 在分析系统中使用受控伪名化。必须支持 tenant export/delete、数据访问/更正、legal hold、备份过期删除和删除证明。高敏 tenant 可使用独立区域和 customer-managed key。

## 12. 高可用、灾备与区域 fencing

### 12.1 区域内

- PostgreSQL 使用托管多 AZ、同步 standby、自动 failover、连续 WAL 和 PITR。
- Redis 使用 TLS、ACL、多 AZ primary/replica；允许全量丢失并从 PostgreSQL 与连接重建。
- 无状态服务跨 3 AZ，反亲和并保留 N+1 容量。
- Gateway 失去一个 AZ 后，其余实例在重连风暴下仍不超过实测安全容量的 70%。

### 12.2 跨区域

初始生产采用一个可写 home region 和一个 warm standby，不做 active-active。异步复制可能让 lease_epoch、session_generation 和 device_sequence 回退，因此引入 control_plane_epoch：

- 独立强一致仲裁存储维护唯一 active-region lease，包含 region、control_plane_epoch、term、lease_until。候选默认 TTL 15 秒、每 5 秒续期，最终值由分区测试冻结。
- Control API mutation、Scheduler 创建 attempt、GetDelivery、Gateway dispatch 和 MarkDispatched 每次都验证未过期 active-region lease；丢失后立即 fail closed。缓存的 dispatch permit 有效期不得超过 region lease 剩余时间。
- epoch 在区域提升时原子递增，并持久化到 PostgreSQL、设备执行日志、session、lease、command 和 ACK。
- 设备持久保存见过的最高 epoch，拒绝更低 epoch。
- DR 提升前必须撤销/等待旧 lease 和所有 permit 过期、隔离旧区域、获取更高 epoch、提升数据库并恢复身份/密钥依赖。
- 无法证明旧区域已被隔离或无法获取新 epoch 时，备用区只提供只读和遥测收集，禁止控制写入。
- 仲裁存储不可用时，已有区域只能在当前 lease 有效期内继续控制；到期后 mutation/dispatch 全部 fail closed，查询和非授权遥测可以继续。

### 12.3 RTO/RPO

| 场景 | RTO | RPO |
|---|---:|---:|
| 单服务实例 | 2 分钟内 | 权威数据 0 |
| 单 AZ | 10 分钟内 | 已确认 PG 事务 0 |
| PostgreSQL primary failover | 5 分钟内 | 同步提交事务 0 |
| Redis 集群全失 | 5 分钟内恢复实时层 | presence/telemetry 可丢；accepted command 0 |
| 全区域 | 60 分钟内 | 最多 1 分钟；仅完成 epoch fencing 后恢复控制 |
| 逻辑误删/数据库损坏 | 4 小时内 | PITR 最多 15 分钟 |
| 身份密钥失陷 | 15 分钟内阻断新使用 | 不适用，audit 不得丢 |

演练的 T0 是故障注入时刻；真实事故的 T0 是监控首次记录用户影响的时刻，另行记录从实际故障到告警的 MTTD。RTO 在目标 N-1 负载下首次恢复核心 SLI、合成命令/回放成功且即时一致性探针通过时结束；进入 DEGRADED 或仅进程启动不算恢复。恢复后还必须连续 15 分钟满足同一组条件，作为 post-recovery validation；该窗口失败会判整次演练失败，但不回加到已经量出的 RTO。RPO 使用 API 已 ACK 事务的 PostgreSQL commit timestamp 与恢复库实际缺失集合计算，不使用备份任务时间。

全区域 60 分钟 RTO 会超过 99.95% 单月约 21.6 分钟的预算；这是明确接受的灾难性预算超支，触发发布冻结和事故复盘，不作为 SLO 排除项。Redis 30 分钟中断是超出 5 分钟 RTO 的韧性压力测试，不能充当 RTO 达标证据。

DR 数据库缺失、但能从设备 execution journal、客户端持久 receipt、旧区 audit/备份或外部 adapter operation token 发现的命令进入 UNKNOWN reconcile 集合。新区域在读取这些证据或人工确认前不得重发。没有任何幸存证据的已 ACK 命令无法被恢复系统枚举，必须计入实测区域 RPO，不能声称已进入 UNKNOWN；若产品要求所有已 ACK 命令均可枚举，则 acceptance ledger 必须跨区域同步提交并把该路径 RPO 提升为 0。

RPO/RTO 是候选目标，只有恢复演练达到后才能成为对外承诺。

### 12.4 备份恢复

- 连续 WAL + 定期 base backup，加密并跨账户/故障域保存。
- 每周自动恢复到隔离环境，校验 schema、约束、关键行数和领域不变量。
- 恢复环境必须完成真实 WS→Dispatcher→gRPC→ACK E2E。
- 每季度人工 DR 覆盖 DNS、流量切换、epoch、证书、KMS、IaC、值班交接和业务验收。
- 定期从空 Redis 启动，证明 presence、alive、streams 和未发布 outbox 可重建。

### 12.5 Redis 全量丢失后的重建

1. 将 transport 状态标记 DEGRADED，REQUIRE_ONLINE fail closed。
2. 当前 Gateway 重建 instance alive；设备重新建立 session/presence，旧 session 不被复用。
3. Event projector 从 PostgreSQL high watermark 恢复 live publication；浏览器通过 durable replay 补缺口，不要求 Redis 保存历史。
4. Scheduler 扫描非终态 command 和 command_work。PENDING_TRANSPORT 可以面向最新 session 创建新 attempt；DISPATCHED/UNKNOWN 必须先按命令安全分类查询设备执行日志或 VendorDriver 状态。
5. 已终态 command 不重新执行。重复 event/delivery 使用稳定 event_id/delivery_id 去重。
6. outbox、PEL、presence 和浏览器 offset 的一致性探针全部通过后才解除 DEGRADED。

## 13. 故障语义

| 故障 | 必须行为 | 强制验证 |
|---|---|---|
| PostgreSQL ambiguous commit | 同 idempotency key 查询/重试 | commit 边界断连接 |
| attempt 提交、Redis 未发 | delivery outbox 重发同 delivery_id | commit 与 XADD 间 kill |
| Redis 全量丢失 | PG 事实不变，实时层重建 | 替换空 Redis 集群 |
| 双 Dispatcher/旧 leader | term fencing 拒绝旧写 | 分区 leadership 连接 |
| Gateway 非对称分区 | instance/session fencing，旧流不能发送 | 单向丢包与半开 TCP |
| side effect 后设备断电 | durable log 重放结果或 UNKNOWN | ACK 前断电 |
| IdP/JWKS 故障 | 已验证短期 token 有界工作，未知 kid fail closed | JWKS 超时和轮换 |
| KMS/Secret Manager 故障 | 有界缓存已有密钥，不签发无限期身份 | deny/timeout |
| 同证书并发设备 | quarantine、驱逐和告警 | 复制证书连接 |
| CA/issuer/cursor key 轮换失败 | 旧 key 有界重叠，未知 key fail closed，不丢授权边界 | 轮换、撤销和回退演练 |
| active-region 仲裁不可用 | lease 到期后控制 fail closed | deny、分区、双重提升和 failback |
| 不兼容迁移 | expand-contract 和应用回滚 | N/N-1 canary |
| hot tenant/重连风暴 | quota、公平队列、其他 tenant SLO 保持 | N-1 容量 chaos |
| 磁盘/连接池耗尽 | 拒绝或 fail closed，不错误成功 | 资源故障注入 |

## 14. SLO 与错误预算

安全不变量没有 error budget：

- 未授权、stale lease/session/region 命令副作用为 0。
- 错误标记 SUCCEEDED 为 0。
- home region 已确认持久化命令丢失为 0。
- 支持幂等的 adapter 重复业务副作用为 0。

初始服务 SLO：

| SLI | 目标 |
|---|---|
| Control mutation API 月可用性 | 99.95% |
| Device Gateway 合成连接/心跳月可用性 | 99.95% |
| Fleet 查询 API 月可用性 | 99.9% |
| 浏览器可靠事件 2 秒内 ACK | 月度 ≥99.0% |
| eligible 在线命令 1 秒内 MarkDispatched | 月度 ≥99.0%，且 p95 ≤500 ms |
| Gateway/AZ 故障后设备恢复 | 95% ≤ 15 s，99% ≤ 30 s |
| 稳态 outbox oldest age ≤5 秒 | 月度时间样本 ≥99.0% |
| 10k event replay 5 秒内完成 | 月度 run ≥99.0%，持久事件缺失为 0 |
| Redis 30 分钟中断后的 backlog | 15 分钟内清空，accepted command 丢失为 0 |

SLI 计算口径：

- HTTP availability = good responses / valid requests，按 UTC 自然月计算。valid request 是身份、schema 和已公布 quota 均有效的请求；业务 CONFLICT/STALE/UNSUPPORTED 是 good response，5xx、网关超时、内部过载 429 和连接失败是 bad。客户端无效 4xx 和租户主动超额不进入分母；计划维护不排除。Control mutation 与 Fleet query 使用独立 recording rule，不加权合并。
- Device Gateway availability 由每区域外部合成设备每 30 秒执行一次完整 mTLS handshake、DeviceHello 和三个 heartbeat 周期；全部在协议 deadline 内成功为 good probe，否则为 bad。真实设备 connection-minutes 另作诊断，不能用客户端断网稀释合成端点 SLI。
- command dispatch 从 command 接受事务的数据库 commit timestamp 到 MarkDispatched 事务 commit；样本只含接受时 session/presence 有效的 REQUIRE_ONLINE 命令。每个样本在 1 秒内完成为 good，否则为 bad；同一集合计算 p95 500 ms 诊断门禁，未 dispatch 样本以 deadline 值进入 percentile 并计 bad，不能静默消失。
- browser reliable-event 样本从 domain_event commit 到浏览器 SDK 对相应 opaque cursor 发送 event.ack；2 秒内 ACK 为 good，没有 ACK 为 bad。页面渲染延迟作为 RUM 辅助指标，不混入该 SLO。
- reconnect denominator 是故障前两个心跳周期内健康、由目标 Gateway/AZ 故障断开的 session；设备侧网络不可达另列，不从受控 chaos 的 denominator 删除。
- outbox oldest age 每 30 秒采样一次当前最老 READY 或 lease 已过期行的 DB-time age；≤5 秒为 good 时间样本，没有 backlog 时为 good。
- replay run 固定为 10,000 个平均不超过 1 KiB 的 domain events、100 个并发 reconnect、filter 命中 20%、N-1 拓扑；5 秒内完成且无缺失/越权为 good，超时为 bad，事件缺失另触发零容忍门禁。
- Redis backlog SLI 使用第 16 节 envelope、A_live/A_recovery 和恢复开始/结束的权威 outbox/command 计数。

端到端设备执行延迟按 adapter 分类，不能把厂商物理动作时间算作统一平台 SLO。burn rate 只对上述已定义 binary good/bad 的 ratio SLI 计算，各自使用 `1-target` 作为错误预算，不把 percentile 直接代入。告警规则为：14.4× burn 同时满足 1 小时和 5 分钟窗口，或 6× 同时满足 6 小时和 30 分钟窗口时 page；3× 同时满足 1 天和 2 小时窗口，或 1× 同时满足 3 天和 6 小时窗口时 ticket。零容忍安全不变量首次命中立即 page 并停止发布；错误预算耗尽时冻结非可靠性发布。

## 15. 可观测性

### 15.1 Trace

完整链路：

WS/REST request → Control API transaction → domain event/outbox → Dispatcher claim → Redis → Gateway → gRPC → device ACK → Control API → event projector → WS replay/live。

关联字段包括 tenant_id、command_id、device_id、control_plane_epoch、lease_epoch、session_generation、gateway_instance、attempt、delivery_id 和 trace_id；不记录 token、证书或完整敏感 payload。

### 15.2 Metrics

- command 状态、延迟、attempt、late ACK、UNKNOWN、reconcile。
- outbox READY/LEASED/DEAD、oldest age、claim latency。
- Redis PEL、consumer lag、stream size、publish errors。
- Gateway 连接、重连、ownership conflict、queue bytes、1013。
- replay offset gap、catch-up latency、resnapshot。
- PG transaction/deadlock/lock wait/replication lag/WAL/disk/pool。
- auth deny、RBAC/ABAC deny、rate limit、证书到期/吊销。
- 每区域持续运行低频合成设备和操作员命令探针。

每个 paging alert 必须包含 owner、严重级别、runbook、用户影响和已验证的触发/恢复方法。

### 15.3 Audit

audit 使用 append-only 数据库权限，并异步导出到 WORM/object-lock 存储。生产服务账户不能 UPDATE/DELETE 历史 audit。记录 actor、delegation、策略版本、资源、请求摘要、decision、result、trace 和时间，不保存秘密。控制 mutation 无法在同一事务写入 audit 时必须失败，不能先成功后补审计。

## 16. 容量模型

所有负载报告记录以下参数：

- D：设备连接数；H：心跳周期；T：每设备 telemetry/s。
- O：操作员 WS 数；S：每操作员订阅设备数。
- C_live/C_outage：正常和故障期间 commands/s；A_live/A_recovery：正常和恢复期平均 deliveries/command。
- E：每个命令产生的平均 domain events；Q：每事件平均存储字节数。
- Bg/Bw：gRPC/WS 平均编码字节数。

派生量：

- gRPC 上行率 = D/H + D×T。
- WS 订阅数 = O×S。
- 均匀订阅 telemetry fan-out ≈ O×S×T。
- live delivery rate = C_live×A_live。
- Redis 中断 t 秒的 delivery backlog = C_outage×A_recovery×t。
- 要在 R 秒内恢复，Dispatcher 最低 delivery 能力 = C_live×A_live + backlog/R。
- domain event publication rate ≈ C_live×E + session/lease/audit events；恢复能力用相同 backlog 公式单独计算。
- 热存储原始量 ≈ event_rate×Q×retention_seconds；容量计划再乘索引、WAL、replica、备份和膨胀系数，不能只算 payload。

原型标准负载仍为 1,000 设备、250 WS、50 commands/s。初始生产规划 envelope 为：

- 10,000 个设备流；
- H=5 秒、T=1 telemetry/s，telemetry 平均 1 KiB、最大 16 KiB；
- 2,000 个 Operator WS，每人 S=20 个订阅，WS event 平均 1 KiB、最大 64 KiB；
- 100 commands/s 稳态，500 commands/s 突发 5 分钟；
- command envelope p95 不超过 4 KiB、最大 64 KiB；simulator adapter latency 为 p50 50 ms、p95 250 ms、p99 1 s；
- 命令测试混合为 20% QUERY_ONLY、60% REPLAY_SAFE、15% RECONCILABLE、4% NON_RETRYABLE、1% SAFETY_CRITICAL；
- 10,000 telemetry/s 入口，按 topic 合并后 fan-out；测试同时覆盖 Zipf tenant skew、单 tenant 20% 流量和热点设备 10% WS 订阅；
- 失去一个 AZ 后仍维持规划峰值。

这些是容量规划输入，不是实测声明。100 commands/s 下 Redis 中断 30 分钟会产生 180,000 条命令；若 A_recovery=1 且要求 15 分钟清空，最低 delivery 能力为 300/s。若中断期间还包含 5 分钟 500/s 突发，backlog 为 300,000，最低约 433/s；重试、event publication、N-1 和安全余量另计。

生产配额不能超过 N-1 拓扑实测安全容量的 65%。break point 定义为升压过程中首次出现以下任一项的最低 offered load：核心 ratio SLO 失败、资源达到批准硬上限，或停止升压后 15 分钟 backlog 仍不单调收敛。每个负载级运行至少 30 分钟、独立重复 3 次；安全容量使用三个 break point 的最小值，不用均值或单次乐观结果。CPU、内存、FD、PG lock/pool、WAL/备份增长、Redis lag、网络和队列全部纳入。GA Release Candidate 固定执行 24 小时 N-1 soak；PR/nightly 的 8 小时结果不能替代 GA 门禁。soak 最后 12 小时对 heap、goroutine、FD 和连接数做每 5 分钟采样，其稳健线性斜率的 95% 置信区间必须包含 0，且终值不超过批准硬上限的 70%。

## 17. 发布、迁移与供应链

### 17.1 数据库变更

- 使用正式 migration version ledger 和独立 migration job。
- expand → 双写/回填 → 读切换 → contract。
- 大表索引使用 online/concurrent 方式，设置 lock_timeout 和 statement_timeout。
- 应用保持 N/N-1 schema 与 Protobuf 兼容。
- 生产回滚回滚应用和流量，不执行 destructive down migration。

### 17.2 协议演进

- Protobuf、内部 API 和 WebSocket envelope 均显式版本化。
- Redis delivery envelope 显式声明 v1-legacy-payload 或 v2-delivery-reference。Gateway 在 instance registration/alive 中声明可读版本，Publisher 只向兼容 instance 的独立 stream 写对应格式；不能用 optional Protobuf 字段假装兼容破坏性语义变化。
- 新字段先按 optional 发布，旧客户端未知字段可忽略；服务端至少兼容 N/N-1。
- ServerHello 返回 server version、最低支持版本和启用的 feature flags；不兼容设备收到明确升级/隔离原因。
- control_plane_epoch、delivery_id 等新 fencing 字段先完成双读/双写和观测，再设为生产强制。
- Buf breaking 以 main 的已发布 schema 为基线；生成代码差异和 adapter contract 是合并门禁。
- 删除字段先停止写、证明无读者、经过一个完整弃用窗口，最后保留 field number/name 防止复用。
- WS cursor 中的 schema version 与 event payload version 分开；Replay API 能按客户端能力升级或明确拒绝，不静默改变语义。

v2 delivery rollout 顺序固定为：先部署能双读 v1/v2 且具备受控 workload mTLS 的 Gateway；验证 capability registry；再让 Publisher 按 instance 写 v2；drain 只支持 v1 的 Gateway 并清理/隔离其 PEL；最后停止 v1 发布。回滚时 Publisher 可在 contract window 内恢复 v1，但同一 delivery 不能同时以 v1 payload 与 v2 reference 面向同一设备执行。N/N-1 Gateway/Publisher 混版、PEL drain 和回滚是发布门禁。

### 17.3 Canary

发布使用签名 image digest：

1. 预生产合成流量；
2. 独立测试 cell 或 1% 设备，15 分钟；
3. 5%，30 分钟；
4. 25%，60 分钟；
5. 50%，120 分钟；
6. 100%，保留 24 小时自动回退观察窗口。

每级至少有 100 个 eligible command 样本；不足则延长观察。baseline 固定为同 region、同 workload class 的上一生产 digest 最近 7 个完整日，排除窗口必须由事故记录批准。所有 promotion/stop 指标仅对 canary cohort 单独计算，并要求正式 SLO 全部成立。自动停止条件：任何重复副作用或错误 SUCCEEDED；page 级 SLO burn；command error/UNKNOWN 比 baseline 高 0.5 个百分点或超过 baseline 2 倍；eligible dispatch 1 秒成功率 <99%；outbox ≤5 秒 good-sample 比例 <99%；故障注入后 reconnect 15 秒内 <95% 或 30 秒内 <99%；PG deadlock 非零；PEL/lag 连续增长 10 分钟；崩溃循环或关键安全告警。Dispatcher 使用独立 shard/cell canary 或只读 shadow，不能让两个版本竞争同一未分片工作域。rollback 的 T0 是自动停止条件首次成立的时间，不是人工确认时间。

应用回滚目标为 15 分钟；Dispatcher shard handoff 无工作窗口不超过 10 秒；Gateway drain 后 99% eligible device 在 30 秒内恢复。回滚后必须验证 N/N-1 schema、未发布 outbox、command/work/attempt 不变量、连接和真实命令 E2E。数据库 contract migration 只能在 100% 稳定观察和一个完整回滚窗口后单独执行。

### 17.4 软件供应链

- GitHub Actions、基础镜像、工具和依赖固定 immutable SHA/digest。
- 发布生成 SPDX/CycloneDX SBOM、SLSA provenance 和 Cosign keyless 签名。
- 部署入口验证签名、来源和允许的 Git ref。
- CI 包含 govulncheck、staticcheck、CodeQL、OSV/npm、容器、secret 和许可证扫描。
- 高危 CVE 默认阻断；例外必须有 owner、补偿控制和到期日。
- branch protection、CODEOWNERS 和双人审阅覆盖 auth、协议、migration、IAM 和生产部署。
- 容器非 root、只读根文件系统、drop capabilities、seccomp、资源限制和最小出站网络。

### 17.5 从当前原型切换

切换不是直接启动新 Dispatcher。进入最终 barrier 前，兼容版本必须已经对每次 command mutation、ACK、reconcile 双写新 command_work/domain_event/event_publication，并连续一个最大命令生命周期通过 shadow 校验；否则最终 barrier 期间暂停新 command mutation，ACK/设备 receipt 仍必须继续并写新事件。

1. 部署兼容代码和新 schema，完成可重入历史 backfill；每批使用稳定主键 watermark 和 INSERT ... ON CONFLICT DO NOTHING，崩溃后从已提交 watermark 继续。
2. 在单个数据库 barrier 事务中写 migration_barrier=FINALIZING、锁定全部 scheduler partition 并原子递增 term。旧 worker 的后续条件写全部失败；command/ACK 双写路径读取 barrier generation。该事务不把稍后的 snapshot 和旧 watermark 拼接。
3. 等待旧 claim lease 到期，再执行可重入 final sweep，反连接 commands、command_work、attempt、旧 outbox 和 domain_events；重复扫描直至两个连续周期缺失数为 0。PENDING_TRANSPORT 生成首个 ROUTE work；DISPATCHED、POSSIBLY_SENT 和 ACK_TIMEOUT 映射为 UNKNOWN_OUTCOME/RECONCILE，不自动重发；终态 command 不生成 work。
4. 旧 outbox 不能统一转换：未发布 command.created 作为旧事件发布意图处理；已发布行只保留审计；XADD 可能成功但 PG 未标记的行视为 ambiguous transport，按 command 安全分类 reconcile。
5. 当前历史无法可靠伪造成完整 domain event。每个 tenant 使用一个 REPEATABLE READ 事务：先 SELECT tenant_event_head FOR UPDATE 以固定 H 并阻塞该 tenant 新事件提交，再在同一 MVCC view 读取授权 snapshot，插入 tenant.snapshot.baseline(H+1) 并签发 cursor(H+1)，最后提交释放 event head。这样 snapshot 与 watermark 必属同一视图；后续双写 mutation 从 H+2 连续发生。旧历史仍可查询/audit，但 durable replay 从 baseline 开始。
6. 清理或隔离旧 Gateway PEL，验证 v2 capability 后启用新 Scheduler/Publisher；用 shadow diff 验证新旧决策，但只有新 term 可写。
7. 在一个事务把 barrier 置 ACTIVE，解除新命令暂停。任何步骤崩溃都从持久 barrier state/watermark 和幂等 sweep 恢复，不能重置 term 或重新生成 baseline。
8. 完成真实命令、迟到 ACK、Redis 重建、N/N-1 混版和回滚 E2E 后结束迁移观察。

RLS tenant context 只允许在显式事务中 SET LOCAL，连接归还 pool 前事务必须结束。CI 对连接复用、panic/rollback、后台任务和运维角色执行跨租户污染测试。

## 18. 验收环境与测试门禁

逐场景的 mode、trigger、oracle、阈值、证据和 owner 见 [生产验收矩阵](production-test-matrix.md)。本节只定义环境层次和全局门禁。

| 环境 | 目的 | 频率 |
|---|---|---|
| PR CI | lint/race/contract、真实临时 PG/Redis、领域不变量 | 每 PR |
| Nightly HA Lab | 多副本、outbox crash window、leader/instance fencing | 每夜 |
| Release Candidate | 生产同型 TLS/OIDC/mTLS/KMS、三 AZ、迁移 | 每候选版本 |
| Soak/Chaos | nightly 8h；GA RC 24h、N-1、网络/资源故障、重连风暴 | 每周/大版本前 |
| DR | PITR、空 Redis、区域提升、epoch、IaC | 自动每周；人工每季度 |
| Production Canary | 隔离 cell/tenant/设备组 | 每次发布 |

上线阻断测试：

- 命令成功、失败、过期、取消、UNKNOWN 和迟到 ACK 的真实传输 E2E。
- attempt/outbox/XADD/ACK 每个提交边界 kill 测试。
- 两个租户的 API、WS、cursor、Redis 和 timing 隔离负向测试。
- 10,000 次重复投递、进程重启和 ACK 丢失，支持幂等 adapter 副作用为 0。
- PG failover、Redis 全失、非对称网络分区、设备断电和旧区域复活。
- 慢 WS 客户端对其他流量 p95 影响不超过 20%。
- migration 从当前生产版本滚动升级并回滚应用。
- 证书、OIDC key、KMS key 和 secret 双窗口轮换。
- 备份恢复后的完整命令 E2E。

每次正式验收生成 run_id，并保存 Git SHA、镜像 digest、SBOM/签名、IaC/migration hash、拓扑、负载参数、随机种子、原始测试、histogram、pprof、故障时间线、脱敏数据库/PEL 快照和实测 RPO/RTO。失败结果不可覆盖；修复后生成新 run_id 并关联。

## 19. 运维与事故控制

生产必须提供经过演练的 kill switch：

- 按 tenant/site/device/capability 禁止新命令；
- 禁止自动重试 NON_RETRYABLE/SAFETY_CRITICAL；
- 强制撤销租约并提升 lease_epoch；
- quarantine 设备证书并驱逐 session；
- Control API 进入只读模式；
- 暂停 Dispatcher claim，但保留 outbox；
- 强制 WS resnapshot；
- DR 区只读/可写切换受 control_plane_epoch 门禁。

值班 runbook 至少覆盖：PG failover、Redis 全失、outbox backlog、UNKNOWN 激增、重复副作用、证书泄漏、JWKS/KMS 故障、hot tenant、Gateway 重连风暴、migration rollback 和区域提升。

## 20. 实施分期

### Phase 0：冻结正确性基线

- 把当前真实 PG/Redis/WS/gRPC 和故障测试自动化进 CI/Nightly。
- 冻结 tenant identity abstraction、完整 command/work/attempt/delivery 状态机、request fingerprint、命令安全分类和 UNKNOWN 语义。
- 接受 ADR-013–016，并冻结 v1/v2 Redis envelope、Gateway capability、GetDelivery permit/nonce 和跨阶段设备协议；先部署双读 Gateway 与测试环境 workload mTLS。
- 加入 domain-event append primitive、event publication intent、delivery_id、control_plane_epoch/device_incarnation 字段和设备 feature negotiation；此阶段不要求 live projector 或区域仲裁上线。
- 引入 migration ledger，并写出 command/work/outbox claim SQL 与每个 crash point 的后置条件。
- 验收：PR-DOM-001 的模型/DDL/迁移兼容子集、HA-PG-001 的当前 commit-boundary 子集、shadow claim contract 和 fingerprint golden vectors 通过；此阶段不要求尚未实现的 authorization、term-fencing 或 forced-RLS 运行时测试，当前原型能力无回归。

### Phase 1：tenant 与权威投递

- tenant-aware schema/RLS/Redis keys。
- attempt + delivery outbox 同事务、dispatch authorization、claim lease、DEAD 隔离、scheduler term fencing，并在所有状态变化事务写最小 domain event；GetDelivery 与 Gateway/Control 间 workload mTLS 在本阶段成为硬门禁。
- 自动重试暂只开放 QUERY_ONLY，以及厂商/设备已经独立证明幂等的 REPLAY_SAFE；其他分类进入 UNKNOWN/人工 reconcile。
- 验收：PR-DOM-001 全量、HA-DEL-001、HA-SCHED-001、SEC-TENANT-001、REL-MIXED-001、Redis 全失、双 Dispatcher 和 hot tenant 测试通过。

### Phase 2：持久事件与浏览器恢复

- tenant offset、domain event、Replay API、无缝 replay/live、WS 背压。
- 验收：断线、Redis 故障和 Gateway 切换时持久事件无缺口。

### Phase 3：设备持久幂等与对账

- device SDK/Bridge WAL、安全分类、VendorDriver query/operation token。
- 设备持久比较 Phase 0 已加入的 control_plane_epoch 和 feature bits；完成后才开放 RECONCILABLE 自动查询/补偿。
- 验收：side effect 后断电和进程重启的 10,000 次测试副作用为 0；无法证明时进入 UNKNOWN。

### Phase 4：生产身份与数据保护

- OIDC/BFF、全工作负载 mTLS 策略与轮换、设备 PKI、KMS/Secrets、ABAC、数据保留；Phase 1 的 GetDelivery mTLS 从受控发行提升为正式 workload identity 平台。
- tenant identity abstraction 在此前阶段使用受控测试 issuer；本阶段替换为 OIDC/mTLS 并冻结生产授权 policy version。
- 验收：身份/证书/密钥轮换、吊销、渗透和跨租户测试通过。

### Phase 5：可观测性与 SLO

- OTel、业务指标、audit WORM、合成探针、burn-rate 告警。
- 验收：每个强制 failure mode 可被信号发现并关联到 command_id。

### Phase 6：HA、DR 与发布

- 三 AZ IaC、managed PG/Redis、drain、canary、SBOM/签名、备份恢复和 active-region lease/epoch 仲裁；epoch 字段和设备持久比较已在 Phase 0/3 完成。
- 验收：N-1、soak、季度 DR 和供应链门禁通过。

Phase 0–6 的 pre-canary 阻断项全部关闭并通过 Pre-canary Go 后，才能进入受控 production canary；每级 promotion 和 100% 后 final closure 使用验收矩阵中的独立决策点，不能提前宣称发布完成。

## 21. Production Go/No-Go

Go 决策需要以下四方分别签字：

- Security：身份、威胁模型、密钥、供应链、渗透和审计。
- Data/Privacy：分类、保留、删除、地域、legal hold 和备份。
- Reliability/SRE：SLO、容量、HA、DR、告警、runbook 和值班。
- Device/Adapter：幂等、对账、安全分类和真实硬件边界。

以下任一成立即 No-Go：

1. 仍使用静态共享 token 或明文设备身份。
2. 共享 SaaS 仍无 tenant-aware schema/RLS。
3. 浏览器 cursor 不能从 PostgreSQL durable replay。
4. 设备重启后无法证明幂等，且危险命令仍会自动重试。
5. attempt 与 delivery intent 不在同一数据库事务，或 poison row 无隔离。
6. 旧 Dispatcher、Gateway、session 或 region 没有可验证 fencing。
7. PG/Redis HA、PITR、空 Redis 重建或区域演练没有证据。
8. dashboard 仍是无真实应用指标的脚手架。
9. 生产制品没有不可变 digest、SBOM、签名和 provenance。
10. 规划容量没有 N-1、soak 和拐点实测。
11. 区域 failback、双重提升、仲裁不可用或旧区域复网没有演练。
12. tenant export/delete、legal hold、备份到期删除和跨租户日志/指标泄漏没有证据。
13. audit 写失败未 fail closed，或 WORM 导出/篡改检测没有验证。
14. kill switch、人工 UNKNOWN reconcile 和 SAFETY_CRITICAL 双人审批没有现场演练。
15. readiness/drain、滚动升级和依赖降级没有真实长连接测试。
16. OTel/日志/指标管道阻塞会反压控制链，或关键 paging MTTD 超过 5 分钟。
17. admission policy 不能实际拒绝未签名、错误 provenance 或禁止漏洞制品。
18. RLS 连接池污染、后台任务/运维角色或每个真实 Adapter 的断电/存储满测试未通过。

## 22. 待外部批准的参数

以下不是架构空白，但必须由组织角色确认后冻结到 ADR：

- 云平台、home/DR region 和数据驻留范围；
- OIDC/MDM/PKI 提供方与设备 enrollment 方式；
- 各数据类型最终保留期限和客户删除承诺；
- 全区域对外 RPO/RTO；
- 首批真实设备、厂商 API 合法性和安全分类；
- 高监管租户使用共享 cell 还是独立 cell；
- on-call ownership、错误预算和发布审批人。

所有这些参数确定后，应在 docs/adr 中形成不可变决策记录；变更通过新 ADR 取代，不直接改写历史理由。
