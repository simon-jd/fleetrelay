# ADR-015：持久事件、snapshot watermark 与 replay/live

- 状态：Proposed；Phase 0 exit 前必须 Accepted
- 日期：2026-09-01
- Owner：Realtime / Control Plane
- Reviewers：Security、Reliability

## 背景

Redis 和 WebSocket 都可能丢失。浏览器需要从 PostgreSQL 权威历史恢复，同时不能通过 cursor 或隐藏 offset 推断未授权对象。

## 决策

- 每个 tenant_event_head 在业务事务中分配无空洞、单调 offset；同事务写 domain_event 和 publication intent。
- 初次订阅在同一 REPEATABLE READ 事务以读取 tenant_event_head 作为建立 MVCC snapshot 的首个业务查询，得到 high-watermark H 后读取授权 snapshot 并签发 cursor(H)；状态与 event 同事务保证两者在该视图一致，无需阻塞事件写入。
- resume cursor 包含 tenant、last offset、canonical filter hash、policy version、schema version、issued time、key id，并加密签名。
- Gateway 内部处理完整 tenant 序列；未授权或 filter 排除的事件不发送，但会推进内部 offset。
- 从 C+1 replay 到 H，同时缓冲 live；任何 gap 先查 PostgreSQL，追平 durable/live head 后再切 live。
- buffer 溢出、cursor 过期、policy 变化或无法追平时返回 RESNAPSHOT_REQUIRED，不静默跳过。
- Redis 只保存可重建短窗口；稳定 event_id/offset 用于去重。每个 Gateway 独立读取全量 relevant tenant stream，不用共享 consumer group 分摊事件。

## 顺序与背压

不能在确认 H+1 已发送或被授权过滤前发送 H+2。可靠队列同时限制条数和字节；超高水位返回 1013 和最后已发送 cursor。telemetry 使用可合并队列，与可靠领域事件隔离。

## 安全后果

低权限客户端不看到数值 offset。opaque cursor 加随机 nonce；固定周期随机化 checkpoint，避免只有隐藏事件发生时才出现可观察流量。授权变更使旧 cursor 失效。

## Rollout、rollback 与验证

先双写 domain event，再部署 Replay API 和双路径 shadow diff，最后启用 WS resume。回滚实时路径不删除 event 表。EVT-REPLAY-001 必须验证 snapshot/H、gap、filter、Redis 全失和 buffer overflow。

## 被否决方案

- 从 Redis replay：Redis 是可删除实时层。
- 每连接轮询 command：负载线性放大且无法覆盖其他操作者事件。
- 暴露 tenant-global offset：会泄漏活动量与隐藏事件间隔。
