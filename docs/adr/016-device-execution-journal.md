# ADR-016：设备执行日志、fingerprint 与 UNKNOWN

- 状态：Proposed；Phase 0 exit 前必须 Accepted
- 日期：2026-09-01
- Owner：Device/Adapter
- Reviewers：Control Plane、Security、Reliability

## 背景

控制平面只能保证 at-least-once delivery。设备或 Vendor Bridge 必须在重启、断电和 ACK 丢失后识别同一业务命令，否则无法安全自动重试。

## 决策

- 每个设备 incarnation 使用 WAL 保存 command_id、Command Fingerprint v1、最高 fences、RECEIVED/EXECUTING/COMPLETED/FAILED/UNKNOWN、result 和 adapter operation token。
- Fingerprint v1 是 SHA-256，覆盖版本、tenant/device/incarnation、command_id/type、RFC 8785 payload、control_plane_epoch、lease_epoch、device_sequence、expires_at；排除 attempt/delivery/gateway/session。
- RECEIVED 与 operation token 在副作用前 fsync；最终结果在 ACK 前 fsync。
- 同 command_id 同 fingerprint 返回原结果；不同 fingerprint 拒绝并 quarantine。
- 重启看到 EXECUTING 时先按 operation token 或 VendorDriver query 对账；不能证明时记录 UNKNOWN，不盲重放危险动作。
- 设备验证签名 permit 的 not_after 和全部 fence；时间来自认证 server-time anchor + 安全单调时钟。无可信时间、WAL 损坏、fsync 失败、磁盘满或最高 fence 丢失时进入 CONTROL_QUARANTINED。
- safe_gc_before 只允许删除已终态结果；最高 epoch、incarnation、last sequence 和防远古重放 tombstone 保留。factory reset 创建新 incarnation 并重新 enrollment。

## Feature negotiation

durable-idempotency-v1、execution-query-v1、control-plane-epoch-v1、delivery-receipt-v1 分别协商。缺少依赖 feature 的 adapter 只能 QUERY_ONLY 或隔离试点。每个真实 VendorDriver 声明 safety class、token、query/cancel/compensate 和错误映射。

## 撤销语义

设备在物理动作开始前复验 permit。lease revoke 提升 epoch、停止新 permit 并发送 signed fence；REVOKED_EFFECTIVE 只在设备确认 fence 或最长 2 秒旧 permit 窗口结束后报告。已开始动作进入取消/对账流程。

## Rollout、rollback 与验证

协议字段先 optional，保存跨语言 golden vectors；服务端按 feature 限制重试。DEV-IDEM-001 对 simulator 自动跑，对每个真实 adapter 人工+自动 contract suite，覆盖副作用后断电、ACK 丢失、WAL 满/损坏和 10,000 次重复投递。

## 被否决方案

- 仅内存缓存：重启即丢失。
- attempt_id 作为幂等键：传输重试会变成新业务动作。
- 设备时钟直接信任 wall clock：可被漂移或回拨绕过 permit 截止时间。
