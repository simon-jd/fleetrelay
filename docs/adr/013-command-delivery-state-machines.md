# ADR-013：命令、工作项、attempt 与投递状态机

- 状态：Proposed；Phase 0 exit 前必须 Accepted
- 日期：2026-09-01
- Owner：Control Plane
- Reviewers：Security、Reliability、Device/Adapter

## 背景

FleetRelay 的设备副作用不能由 Redis、进程内存或“是否收到 ACK”推断。一次业务命令可以经历多次传输，但命令、调度工作、设备执行证据和消息发布是四种不同事实。把它们合并会在崩溃窗口错误重发危险命令。

## 决策

### Command

非终态：PENDING_TRANSPORT、DISPATCHED、RECONCILING、UNKNOWN_OUTCOME。终态：SUCCEEDED、FAILED、CANCELLED、EXPIRED、RESULT_CONFLICT。

合法主路径：

~~~text
PENDING_TRANSPORT -> DISPATCHED -> SUCCEEDED | FAILED | UNKNOWN_OUTCOME
PENDING_TRANSPORT -> CANCELLED | EXPIRED | UNKNOWN_OUTCOME
UNKNOWN_OUTCOME -> RECONCILING -> SUCCEEDED | FAILED | UNKNOWN_OUTCOME
任何互相矛盾的有效结果 -> RESULT_CONFLICT
~~~

SUCCEEDED、FAILED、CANCELLED、EXPIRED 不互相覆盖。唯一例外是有效迟到执行证据可把 UNKNOWN_OUTCOME/RECONCILING 对账为 SUCCEEDED 或 FAILED。已有相反终态时进入 RESULT_CONFLICT。

### Command work

kind 为 ROUTE、RECONCILE、QUERY_STATE；state 为 READY、LEASED、DONE、CANCELLED、DEAD。一个 command 最多一个 active ROUTE 和一个 active RECONCILE。人工复核不是 worker kind。

### Attempt

state 为 CREATED、POSSIBLY_SENT、DISPATCHED、ACKED、ROUTE_TIMEOUT、UNKNOWN、SUPERSEDED。

- permit 在返回 Gateway 前，事务先把 attempt 标为 POSSIBLY_SENT。
- DISPATCHED 只由成功 gRPC write 后的 MarkDispatched，或携带有效 permit 的设备 ACK 产生。
- SUPERSEDED/UNKNOWN 保留 was_dispatched/was_possibly_sent 和 authorization 关联；ACK eligibility 不依赖当前 status。
- ACKED 只表示 ACK 已通过身份、fingerprint 和 fencing 校验。

### Delivery outbox

state 为 READY、LEASED、PUBLISHED、RETIRED、DEAD。它只描述 Redis publication，不代表设备是否收到。Redis 重发复用 delivery_id，不增加 attempt_number。

### Dispatch authorization

state 为 ISSUED、CONSUMED、EXPIRED。唯一键为 tenant_id、delivery_id、nonce，并以 partial unique 保证每 delivery 最多一个 ISSUED。permit 绑定完整身份、fingerprint、fence、session、gateway 和 not_after。GetDelivery 锁 attempt 后以 CAS 返回现有或创建 nonce；不生成并行授权。MarkDispatched 原子执行 ISSUED→CONSUMED 和 POSSIBLY_SENT→DISPATCHED；fast ACK 在一个事务完成 authorization、attempt、command、audit 和 event。

## 模糊窗口的安全决策

| 证据边界 | QUERY_ONLY | REPLAY_SAFE | RECONCILABLE | NON_RETRYABLE | SAFETY_CRITICAL |
|---|---|---|---|---|---|
| permit 尚未签发 | 同 delivery 路由 | 同 delivery 路由 | 同 delivery 路由 | 同 delivery 路由 | 同 delivery 路由 |
| permit 已签发，未 MarkDispatched | 重试 | 仅 durable-idempotency-v1 重试 | QUERY_STATE | UNKNOWN | UNKNOWN + 双人复核 |
| 已 DISPATCHED，无 ACK | 重试 | 仅 durable-idempotency-v1 有界重试 | QUERY_STATE | UNKNOWN | UNKNOWN + 双人复核 |

同一 command_id 不同 fingerprint 是安全事件；同一命令的多个有效 attempt 返回不同结果进入 RESULT_CONFLICT。

## ACK 规则

ACK 可以来自非最新 session/attempt。只要 was_dispatched=true，或 was_possibly_sent=true 且具有可验证历史 ISSUED/CONSUMED/EXPIRED permit，当前为 POSSIBLY_SENT、DISPATCHED、SUPERSEDED 或 UNKNOWN 都可参与迟到对账。EXPIRED 禁止再次授权发送，不删除迟到执行证据。事务同时验证 tenant/device/incarnation、command/delivery/attempt、fingerprint、lease/session/region fence 和设备 mTLS 身份；相反终态进入 RESULT_CONFLICT，重复相同 ACK 返回原结果。

## 后果

- at-least-once transport 不被描述为 exactly-once execution。
- permit 签发后崩溃会保守地产生 UNKNOWN，而不是冒险重发。
- 设备持久幂等能力成为开放自动重试的正式 feature gate。

## Rollout、rollback 与验证

先添加新 enum/列和双写，再启用状态校验，最后移除 legacy ACK_TIMEOUT 写入。回滚应用时保留新列和 UNKNOWN 映射，不执行 destructive migration。HA-DEL-001、DEV-IDEM-001 和跨语言 fingerprint vectors 是接受门禁。

## 被否决方案

- 用 Redis PEL 判断是否执行：PEL 不是设备事实。
- send 后才持久化授权：留下已发送却可被当成未发送的窗口。
- 所有命令统一自动重发：会让危险副作用重复。
