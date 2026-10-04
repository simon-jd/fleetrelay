# FleetRelay ADR register

生产架构决策以 [生产设计基线](../production-design.md) 为总入口。下表记录待工程/组织接受的决策；只有状态变为 Accepted 才算冻结，实现 PR 应引用对应 ADR 编号。

| ADR | 决策 | 状态 |
|---|---|---|
| ADR-001 | PostgreSQL 是唯一权威；Redis 是可重建实时层 | Proposed |
| ADR-002 | at-least-once + 设备端持久幂等，不声明 exactly-once | Proposed |
| ADR-003 | 生产 schema、路由、cursor 和配额全部 tenant-aware | Proposed |
| ADR-004 | 浏览器可靠事件使用 PostgreSQL tenant offset 和 durable replay | Proposed |
| ADR-005 | attempt 与 delivery_outbox 同事务，发布重试不新增 attempt | Proposed |
| ADR-006 | ACK_TIMEOUT 解释为 UNKNOWN_OUTCOME，并按命令安全分类对账 | Proposed |
| ADR-007 | 单 home region、多 AZ、warm standby；提升使用 active-region lease 和 control_plane_epoch | Proposed |
| ADR-008 | OIDC 操作员身份、workload mTLS、每设备 PKI | Proposed |
| ADR-009 | 托管 Kubernetes 三 AZ，不引入 Service Mesh 或第二消息平台 | Proposed |
| ADR-010 | 云平台、home/DR region 与数据驻留 | Pending approval |
| ADR-011 | 最终数据保留、删除和 legal hold | Pending approval |
| ADR-012 | 首批 VendorDriver、厂商 API 与真实设备安全分类 | Pending approval |
| [ADR-013](013-command-delivery-state-machines.md) | Command/work/attempt/delivery/UNKNOWN 完整状态机 | Proposed；Phase 0 exit 前 Accepted |
| [ADR-014](014-transaction-claims-and-crash-boundaries.md) | command_work、attempt、delivery_outbox、authorization、claim 和 crash 后置条件 | Proposed；Phase 0 exit 前 Accepted |
| [ADR-015](015-durable-event-replay.md) | Domain event、snapshot-watermark、cursor 和 replay/live 顺序算法 | Proposed；Phase 0 exit 前 Accepted |
| [ADR-016](016-device-execution-journal.md) | Device execution journal、fingerprint、operation token、UNKNOWN 和 GC | Proposed；Phase 0 exit 前 Accepted |
| [ADR-017](017-prototype-delivery-intents.md) | 原型 attempt + delivery intent 原子提交与稳定 ID 重发 | Accepted（仅原型范围）；ADR-014 生产 claim/permit 仍 Proposed |
| [ADR-018](018-outbox-retry-isolation.md) | 原型 outbox 持久退避、超时隔离、指标及迁移 | Accepted（仅原型范围） |
| [ADR-019](019-local-docker-recovery-and-bounded-dispatch.md) | Docker 故障证据、模拟器持久结果记录及有界 outbox 跟进发布 | Accepted（仅原型/模拟器范围） |

## 新 ADR 模板

每个 ADR 文件命名为 NNNN-short-title.md，至少包含：

- 状态：Proposed、Accepted、Superseded 或 Rejected；
- 日期、owner、reviewers；
- 背景与约束；
- 决策；
- 被否决的备选方案及原因；
- 正确性、安全、运维、成本和迁移后果；
- rollout、rollback 和验证证据；
- 替代或被替代的 ADR。

已接受 ADR 不重写历史理由；新决策通过 Superseded 链接替代。
