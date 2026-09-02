# ADR-014：事务、claim、锁顺序与崩溃边界

- 状态：Proposed；Phase 0 exit 前必须 Accepted
- 日期：2026-09-01
- Owner：Control Plane / Data
- Reviewers：Reliability、Security

## 背景

生产实现必须让并发 worker、进程崩溃、Redis 非事务发布和 PostgreSQL failover 后的结果可判定。本 ADR 冻结最小 schema 约束、claim 协议和每个外部副作用边界。

## 决策

### 必需约束

- 所有业务表 tenant_id NOT NULL；复合 FK 不允许只靠 UUID 穿透租户。
- UNIQUE(tenant_id, operator_id, device_id, idempotency_key) 并保存 request_hash。
- UNIQUE(tenant_id, device_id, device_sequence)。
- UNIQUE(tenant_id, command_id, attempt_number)。
- UNIQUE(tenant_id, delivery_id)；UNIQUE(tenant_id, delivery_id, permit_nonce)；partial UNIQUE(tenant_id, delivery_id) WHERE authorization_state='ISSUED'。
- partial unique：每 command 最多一个 active ROUTE、一个 active RECONCILE。
- attempt 与 delivery_outbox 同事务；业务状态、audit、domain_event、event_publication intent 同事务。
- attempt/outbox 在创建事务保存不可变 envelope_version 与 Gateway capability_generation；同 delivery 的 republish 不重新协商版本。

### Claim 记录

command_work、delivery_outbox 和 event_publication 都包含 owner、lease_token、lease_until、retry_count、next_attempt_at。claim 使用 PostgreSQL DB time、FOR UPDATE SKIP LOCKED 和随机 lease_token。所有完成、续租、重排和 DEAD 更新都以 owner、token、term、lease 未过期为条件；影响行数为 0 代表已经失去所有权。

### 统一锁顺序

~~~text
scheduler_partition
  -> command_work
  -> device / command
  -> command_attempt / dispatch_authorization / delivery_outbox
  -> tenant_event_head
~~~

事务可以从不需要的前缀之后开始，但不得反向加锁。SERIALIZABLE failure、deadlock 和 ambiguous commit 用原 idempotency/token/delivery_id 有界重试。

### 外部副作用边界

| 边界 | 提交前崩溃 | 提交后崩溃 | 恢复动作 |
|---|---|---|---|
| command accept tx | 无 command | command/work/event 全存在 | 原 key 查询 |
| attempt + outbox tx | 两者均无 | 两者均有 | claim 原 work/outbox |
| Redis XADD | outbox READY/LEASED | 消息可能存在、PG 可能仍未 PUBLISHED | 同 delivery_id 重发 |
| permit issuance tx | 无可执行 permit | auth ISSUED、attempt POSSIBLY_SENT | 按安全分类处理 |
| gRPC write → MarkDispatched | POSSIBLY_SENT | 可能收到或已 DISPATCHED | device receipt/ACK；不得统一重发 |
| ACK tx | 无终态 | 状态/event/audit 全存在 | 重复 ACK 返回原结果 |
| event XADD | publication 未 PUBLISHED | event 可能重复 live | 稳定 event_id 重发 |

Gateway 只有在 MarkDispatched 成功，或 Control API 已持久化为“旧 attempt SUPERSEDED + 新 ROUTE work”、RECONCILING 或 UNKNOWN_OUTCOME 后才 XACK。PEL 重领从 GetDelivery 权威状态继续。

### Tenant RLS resolution

Redis v2 envelope 携带不可信 tenant_id + delivery_id。Control API 开显式事务，SET LOCAL tenant context，然后通过 tenant-scoped FK 查询并重新验证 stream shard、gateway instance、session 和全部 fence。跨租户相同 delivery_id 不能命中。

## Crash invariants

1. accepted command 一定有可恢复 work 和最小 domain event。
2. 不存在 attempt 而没有对应 delivery intent。
3. permit 可离开服务前，POSSIBLY_SENT 已提交。
4. 任何旧 term/token owner 的写影响 0 行。
5. DEAD 行可枚举、告警、人工重放或安全终止，不永久阻塞 claim queue。

## Rollout、rollback 与验证

expand schema 后先 shadow claim，不写设备；验证锁顺序和 crash matrix，再分 tenant/cell 启用。应用回滚保留 lease/term/auth 行。Phase 0 exit 只要求状态模型、crash postcondition、迁移兼容、shadow claim 和当前 commit-boundary 子集；完整 HA-DEL-001、HA-SCHED-001 在 Phase 1 exit，DEV-IDEM-001 在 Phase 3 exit。

## 被否决方案

- 进程级 advisory lock 作为唯一 leader：无法跨崩溃证明工作所有权。
- Redis transaction 作为权威：不能和 PostgreSQL 业务事务原子提交。
- 反向锁或无界 deadlock retry：可能活锁并放大故障。
