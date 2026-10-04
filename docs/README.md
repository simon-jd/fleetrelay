# FleetRelay 文档索引

| 文档 | 用途 | 状态 |
|---|---|---|
| [V1 设计规格](../2026-08-31-fleetrelay-v1-design.md) | 产品命题、V1 架构与原始验收目标 | 目标规格 |
| [生产设计基线](production-design.md) | 安全、租户、事件回放、HA/DR、SLO 和上线门禁 | Proposed |
| [生产验收矩阵](production-test-matrix.md) | 可执行场景、oracle、阈值、证据和 owner | Proposed |
| [Phase 0/1 可执行规格](production-tests/phase-0-1.md) | commit、投递、调度、租户与混版测试的完整输入/输出 | Proposed |
| [本地运行手册](runbook.md) | Compose 启停、故障演示和原型排障 | 已验证原型 |
| [控制台协议](console-contract.md) | 当前 HTTP/WS 接口约定 | 原型合约 |
| [验证报告](evidence/validation-report.md) | 独立构建、E2E 和故障测试证据 | 2026-09-01 基线 |
| [ADR register](adr/README.md) | 生产架构决策与待批准参数 | 持续维护 |
| [2026-09-06 岗位与技术核查](market-review-2026-09-06.md) | 官方 JD 日期/在招状态、版本取舍与项目缺口 | 已核查，小样本 |
| [2026-09-06 投递验证](evidence/2026-09-06/report.md) | 事务故障、真实依赖与四进程模拟器链路 | 已在本地验证 |
| [2026-09-07 重试隔离与核查](market-review-2026-09-07.md) | 持久退避、真实积压指标、迁移与最新岗位取舍 | [本地验证](evidence/2026-09-07/report.md) |
| [2026-09-07 Docker 故障与延迟实验](evidence/2026-09-07-docker/report.md) | 持久模拟器结果、容器崩溃恢复、在线告警、逻辑恢复及成对延迟测量 | 本地 Docker / 设备仿真；非生产门禁 |

生产设计描述目标状态，不代表当前代码已经满足。当前能力和限制以 README 与验证报告为准。
