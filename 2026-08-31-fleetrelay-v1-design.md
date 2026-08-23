# FleetRelay V1 设计规格

> Go 实时设备集群控制平面，以及多楼层扫地机器人参考应用

- 状态：已批准的 V1 架构设计，等待用户文档复核
- 日期：2026-08-31
- 设计路径：先构建通用控制平面，再接入家庭扫地机器人
- 目标岗位：北美 Backend、Distributed Systems、Cloud/Infrastructure、Robotics Platform

## 1. 执行摘要

FleetRelay 是一个使用 Go 构建的实时设备集群控制平面。操作员通过浏览器建立 WebSocket 会话，远端设备或边缘网关通过双向 gRPC 流连接系统，所有设备协议由 Protocol Buffers 定义。系统负责控制租约、命令排序、可靠投递、应用层 ACK、断线恢复、跨实例路由、审计与可观测性。

FleetRelay V1 首先证明一个通用系统命题：

> 当浏览器、控制平面和设备连接都可能中断时，系统仍能把授权操作员的命令发送到正确设备，准确呈现命令状态，并阻止旧会话或重复消息造成重复副作用。

多楼层扫地机器人是 V1 的参考应用，而不是写入核心服务的特殊逻辑。设备模拟器和真实 Vacuum Bridge 使用同一套 gRPC/Protobuf 合约。因此，家庭场景可以直观演示系统，同时控制平面仍可扩展到仓储机器人、配送设备、实验室仪器或其他远程设备。

V1 明确采用三项工程原则：

1. PostgreSQL 是控制状态的唯一权威来源。
2. Redis Streams 负责跨实例实时传播，但不决定业务正确性。
3. 交付语义是 at-least-once delivery 加幂等执行，不声称端到端 exactly-once。

## 2. 为什么这个项目值得做

### 2.1 能力缺口

这个项目集中展示以下能力：

- Go 长连接服务和并发控制。
- gRPC 双向流与 Protocol Buffers 合约治理。
- WebSocket 会话、订阅、重连与背压。
- 分布式租约、fencing token 和每设备有序命令。
- Transactional outbox、幂等性和故障恢复。
- PostgreSQL 与 Redis Streams 的职责划分。
- OpenTelemetry、Prometheus、结构化日志和故障演示。
- 以可复现基准生成简历证据，而不是预先编造性能结果。

### 2.2 2026 北美岗位信号

以下是方向性样本，不是市场份额统计；职位内容和有效性会随时间变化：

- Applied Intuition 的 Remote Assistance 后端岗位强调实时数据流、会话管理、Go/Python、Protocol Buffers、gRPC、Redis、网络中断和故障转移。
- Block Labs 的 Go 后端岗位强调内部 gRPC、客户端 REST/WebSocket、contract-first、确定性、幂等性、可审计性和可观测性。
- Planera 的后端岗位把 Go、REST/WebSocket/SSE、Redis、消息系统、Docker、云平台和 Prometheus/Grafana 放在同一技术面中。
- E2B 的实时产品体验岗位使用 WebSocket/SSE 处理日志和指标，并强调可观测性、测试和安全。

FleetRelay 不会因为这些样本而堆砌所有工具。V1 只保留能形成一个完整系统论述的核心技术。

## 3. 目标与非目标

### 3.1 V1 目标

- 操作员可以在浏览器查看设备在线状态、实时遥测、告警和命令状态。
- 操作员可以获得设备控制租约并发送受权限约束的命令。
- 设备通过一个长期存活的 gRPC 双向流上报心跳、遥测、告警和命令 ACK。
- 控制命令在发送前持久化，并具有明确、可查询的状态机。
- 同一设备在任意时刻最多有一个有效控制租约。
- 旧租约、旧设备连接和重复 ACK 不会产生新的业务副作用。
- Gateway 可以水平扩展；设备和浏览器可以连接任意实例。
- Gateway、Redis 或网络发生故障时，系统能恢复且不丢失已经接受的命令。
- 系统提供可重复执行的正常流程、故障注入和负载基准。
- 扫地机器人通过独立适配器接入，不污染核心领域模型。

### 3.2 V1 非目标

- 不实现 SLAM、视觉识别、地图构建或自主路径规划。
- 不实现视频或音频实时传输；这类媒体更适合 WebRTC 等独立数据面。
- 不支持任意厂商扫地机器人的全部私有功能。
- 不逆向闭源厂商协议作为项目核心成果。
- 不实现完整 Home Assistant 替代品。
- 不加入 Kafka、NATS、RabbitMQ 等第二套消息平台。
- 不构建 Kubernetes Operator、多云控制器或 Service Mesh。
- 不声称生产部署、真实硬件规模或未执行过的性能结果。

## 4. 设计原则

### 4.1 控制平面与设备能力解耦

核心系统只理解通用设备、控制租约、命令、ACK、遥测、告警和连接会话。具体设备能力通过 capability 声明和 adapter 映射实现。

### 4.2 正确性优先于即时性

控制命令必须先持久化，再进入实时投递。Redis 不可用时，命令可以停留在 PENDING_TRANSPORT，而不是被错误报告为已经发送。

### 4.3 区分可靠控制与可降级遥测

命令、ACK、租约和审计是可靠控制通道。电量、位置和进度是遥测通道。遥测允许合并、采样和丢弃旧值，不能反向阻塞控制通道。

### 4.4 显式承认重复

数据库提交、消息发布、网络发送和 ACK 返回之间无法形成一个跨系统原子事务。V1 接受重复投递，通过 command_id、idempotency_key、device_sequence、session_generation 和 lease_epoch 识别重复或过期消息。

### 4.5 本地可复现优先

完整系统必须能通过 Docker Compose 在开发机运行。云部署资产只能作为额外证明，不能取代本地演示、测试报告和故障证据。

## 5. 系统架构

~~~text
                    +---------------------------+
                    | React Operator Console    |
                    | fleet / alerts / control  |
                    +-------------+-------------+
                                  | WebSocket + REST
                                  v
+------------------------------------------------------------------+
| Stream Gateway - Go                                              |
|                                                                  |
| WebSocket sessions       gRPC device streams                     |
| topic subscriptions      connection registry                     |
| bounded outbound queues  heartbeat / telemetry / alert / ACK     |
+----------------+----------------------+--------------------------+
                 | internal gRPC        | Redis Streams
                 v                      v
+-----------------------------+   +--------------------------------+
| Control API - Go            |   | Dispatcher - Go                |
|                             |   |                                |
| authorization               |   | transactional outbox consumer  |
| command state machine       |   | command routing                |
| control leases              |   | retry and expiration           |
| audit and replay API        |   | cross-instance event fan-out   |
+---------------+-------------+   +---------------+----------------+
                |                                 |
                v                                 v
        +---------------+                 +---------------+
        | PostgreSQL    |                 | Redis Streams |
        | source of     |                 | realtime      |
        | truth         |                 | transport     |
        +---------------+                 +---------------+

                     bidirectional gRPC + Protobuf
                                  |
                 +----------------+----------------+
                 v                                 v
        +------------------+              +------------------+
        | Device Simulator |              | Vacuum Bridge    |
        | deterministic    |              | vendor adapter   |
        +------------------+              +--------+---------+
                                                   |
                                                   v
                                           Household Vacuums
~~~

### 5.1 部署单元

V1 使用一个 Go monorepo，构建三个主要服务和两个边缘程序：

| 程序 | 职责 | 依赖 |
|---|---|---|
| control-api | 权限、租约、命令状态机、查询、审计、重放 | PostgreSQL、Redis presence 只读检查 |
| stream-gateway | WebSocket 与设备 gRPC 长连接、订阅、本地连接路由、背压 | Control API、Redis |
| dispatcher | outbox 发布、跨实例命令路由、重试、过期处理 | PostgreSQL、Redis、Control API |
| device-sim | 确定性设备模拟、故障注入、负载生成 | Stream Gateway |
| vacuum-bridge | 通用 FleetRelay 协议到具体机器人驱动的转换 | Stream Gateway、VendorDriver |

三个服务是清晰的运行边界，但不会继续拆成大量微服务。领域规则集中在 Control API；Gateway 不绕过 Control API 直接修改控制状态；Dispatcher 直接读取和标记 outbox，但命令状态转换仍通过 Control API 的内部 gRPC 接口完成，不能重新实现授权或状态机。

### 5.2 为什么不用单体服务

一个进程可以更快完成 happy path，但无法有说服力地演示设备连接归属、跨实例命令路由、Gateway 故障转移和浏览器连接重放。三个部署单元是 V1 为了系统命题所需的最小拆分。

### 5.3 为什么不用更多微服务

把认证、设备注册、租约、命令、遥测和审计分别拆服务会引入大量分布式事务和部署噪声，却不会增强 V1 的核心论点。模块边界先在代码中保持清晰，只有独立扩缩容或所有权需求得到实测证明后才考虑拆分。

## 6. 协议设计

### 6.1 设备 gRPC 双向流

~~~protobuf
service DeviceRelayService {
  rpc Connect(stream DeviceToServer) returns (stream ServerToDevice);
}
~~~

设备上行消息：

- DeviceHello：声明 device_id、adapter、capabilities 和客户端版本。
- Heartbeat：维持连接活性并携带当前 session_generation。
- Telemetry：上报电量、状态、位置或应用特定字段。
- DeviceAlert：上报卡住、低电量、传感器故障等事件。
- CommandAck：确认命令成功、失败、拒绝或不支持。

服务器下行消息：

- ServerHello：分配 session_id、session_generation、心跳周期和服务器时间。
- Command：携带命令、顺序号、本次投递的 session generation、租约 epoch、过期时间和幂等键。
- LeaseUpdate：通知设备当前控制 epoch 的变化。
- ServerHeartbeat：帮助设备发现半开连接。
- DisconnectNotice：在计划内关闭时提供原因和重连提示。

### 6.2 Protocol Buffers 治理

Protocol Buffers 使用版本化目录 api/fleetrelay/v1。Buf 负责：

- buf build：保证 schema 可以构建。
- buf format：统一格式。
- buf lint：执行 API 规则。
- buf breaking：阻止不兼容变更进入主分支。
- buf generate：生成 Go gRPC 代码。
- Protovalidate：把必要字段、长度、范围和时间约束写入 schema。

V1 不直接发布到托管 Schema Registry；所有离线检查都能在本地和 CI 完成。

### 6.3 浏览器 WebSocket 协议

浏览器使用版本化 JSON envelope，而不是 Protobuf 二进制帧。理由是浏览器调试、错误检查和演示可读性更重要；Protobuf 仍然作为设备与服务间的强类型合约。

~~~json
{
  "schema_version": 1,
  "message_id": "01J...",
  "type": "command.request",
  "device_id": "vacuum-floor-2",
  "sent_at": "2026-08-31T20:00:00Z",
  "payload": {}
}
~~~

客户端消息包括 authenticate、subscribe、unsubscribe、lease.acquire、lease.renew、lease.release 和 command.request。服务端消息包括 snapshot、device.event、telemetry.update、lease.changed、command.status、error 和 resume.complete。

每个连接只有一个 writer goroutine。业务模块把消息放入有界队列，不能并发直接写 socket。控制事件和告警使用高优先级队列；可覆盖遥测使用按 device_id 加 topic 的最新值缓存。

### 6.4 REST 边界

REST 只承担不需要双向实时状态的操作：

- 开发环境登录和短期访问令牌签发。
- 设备、能力和历史命令查询。
- 审计事件分页查询。
- WebSocket 重连后的持久事件重放。
- 健康检查、就绪检查和管理接口。

V1 不加入 GraphQL；当前领域没有足够复杂的跨资源查询来证明它的额外 schema 与 resolver 成本。

## 7. 核心领域模型

### 7.1 Device

Device 表示逻辑设备身份，与某次网络连接分离。主要字段包括 device_id、display_name、adapter_type、capabilities、desired_status、last_seen_at 和 version。

### 7.2 DeviceSession

每次设备成功建立 gRPC 流都会创建新 session_id，并递增 session_generation。旧 Gateway 即使仍持有半开连接，也不能向设备发送针对新 generation 的命令。

### 7.3 ControlLease

ControlLease 表示某个 operator 对设备的临时独占控制权。V1 默认租约 30 秒，客户端每 10 秒续期；具体数值可配置，但测试使用固定值。

租约包含 device_id、operator_id、lease_id、lease_epoch、expires_at 和 status。每次新的控制者成功获得租约时，lease_epoch 单调递增。命令必须携带创建时的 lease_epoch，设备和服务端都拒绝更旧的 epoch。

### 7.4 Command

命令字段至少包括：

- command_id：全局唯一身份。
- idempotency_key：客户端重试时保持不变。
- device_id：目标逻辑设备。
- device_sequence：设备维度的单调顺序号。
- lease_epoch：授权该命令的控制租约 epoch。
- command_type 和 payload：具体操作。
- delivery_policy：REQUIRE_ONLINE 或 QUEUE_UNTIL。
- expires_at：过期后不得执行。
- status：当前状态。

命令状态机：

~~~text
request +--> REJECTED
        |
        +--> ACCEPTED --> PENDING_TRANSPORT --> DISPATCHED --> SUCCEEDED
                             |                    |            FAILED
                             |                    |            ACK_TIMEOUT
                             |                    |
                             +--> EXPIRED         +--> retry same command_id
                             +--> CANCELLED
~~~

SUCCEEDED 和 FAILED 表示设备返回了应用层结果。ACK_TIMEOUT 只表示系统在重试窗口内没有得到结果，不能推断设备未执行；迟到的有效 ACK 可以把 ACK_TIMEOUT 对账为 SUCCEEDED 或 FAILED，并追加审计记录。重复 ACK 不会再次产生业务副作用。重试生成新的 delivery attempt，不生成新的 command_id。

session_generation 属于 CommandAttempt，而不是不可变 Command。设备重连后，Dispatcher 可以为同一个 command_id 创建指向新 generation 的投递尝试；旧 Gateway 上的迟到尝试仍会因为 generation 过旧而被拒绝。

### 7.5 Event 与 AuditRecord

Event 是供实时订阅和重放的领域变化。event_id 负责唯一身份；由事件投影器分配的 event_offset 负责全局重放顺序；对浏览器暴露的是包含 offset 的不透明 event_cursor。AuditRecord 记录 actor、action、target、request_id、结果、时间和必要的安全上下文。遥测样本不是全部审计事件，只有状态转换、告警和控制相关动作进入持久事件流。

## 8. 数据存储与消息布局

### 8.1 PostgreSQL 表

| 表 | 用途 | 关键约束 |
|---|---|---|
| devices | 逻辑设备与能力 | device_id 唯一 |
| device_sessions | 连接历史与 generation | 每设备 generation 唯一 |
| control_leases | 控制权与 epoch | 每设备最多一个 ACTIVE 租约 |
| device_counters | 分配 device_sequence | 行级锁保证单调递增 |
| commands | 命令权威状态 | command_id 唯一；幂等键作用域唯一 |
| command_attempts | 每次投递、目标 Gateway 和 session generation | command_id 加 attempt 唯一 |
| event_cursor | 事件投影器的下一个 offset | 单例行，事务内递增 |
| domain_events | 可重放领域事件 | event_id 唯一；event_offset 单调 |
| audit_records | 安全与操作审计 | 追加写 |
| outbox | 待发布消息 | outbox_id 唯一、按业务键幂等 |

租约获取、命令创建、命令状态转换与对应 outbox 记录必须在同一数据库事务中提交。

### 8.2 Redis 数据

- relay:presence:{device_id}：带 TTL 的 Gateway owner、session_id 和 generation。
- relay:gateway:{gateway_id}:commands：发往指定 Gateway 的命令 Stream。
- relay:fleet:events：供 Gateway 消费的实时领域事件 Stream。
- relay:telemetry：短保留期的遥测 Stream。
- relay:latest:{device_id}:{topic}：最新遥测快照。

Redis Stream consumer 使用 event_id 或 command_id 去重。删除或丢失 Redis 数据不会改变 PostgreSQL 中的命令、租约和审计事实。

每个 CommandAttempt 记录 target_gateway_id、target_session_generation、routed_at 和 attempt_deadline。Gateway 崩溃或 owner 改变后，Dispatcher 让旧 attempt 超时，再根据最新 presence 创建新 attempt。新 Gateway 不需要消费旧 Gateway 的私有 Stream。

### 8.3 有序事件投影

Control API 的业务事务只写业务状态、audit 和 outbox。Dispatcher 中的单活事件投影器通过 PostgreSQL advisory lock 取得发布权，在一个事务中读取 outbox、锁定 event_cursor、分配 event_offset、写入 domain_events 并标记投影完成。Redis 发布成功与否单独记录；若进程在数据库提交后、Redis 发布前崩溃，Dispatcher 会重新发布同一个 event_id，消费者负责去重。

这个投影顺序是 FleetRelay 对浏览器承诺的事件顺序，不声称等同于所有并发业务事务的墙钟提交顺序。

### 8.4 遥测保留

Gateway 可以合并同一设备同一 topic 的旧遥测。Redis 中只保留短期数据；关键状态变化通过 outbox 投影到 domain_events，普通遥测默认每 30 秒保存一次最新快照。这个周期是 V1 的可配置默认值，不是永久数据保留承诺。

## 9. 关键数据流

### 9.1 设备建立连接

1. 设备用 mTLS 连接任意 Stream Gateway。
2. Gateway 验证证书身份与 DeviceHello.device_id 一致。
3. Gateway 调用 Control API 创建 DeviceSession。
4. Control API 递增 session_generation 并返回 ServerHello 数据。
5. Gateway 注册本地连接，并用 TTL 写入 Redis presence。
6. 设备开始发送 Heartbeat；Gateway 续期 presence。
7. 同一设备的新 generation 生效后，旧连接只允许完成关闭，不再接受命令。

### 9.2 操作员取得控制租约

1. 浏览器通过 WebSocket 发送 lease.acquire。
2. Gateway 将请求和身份上下文转发给 Control API。
3. Control API 在事务中锁定设备租约记录。
4. 若现有租约有效，请求返回 CONFLICT 和当前持有者的非敏感信息。
5. 若可获取，系统递增 lease_epoch、写入租约与审计、追加 outbox。
6. Dispatcher 发布 lease.changed，所有订阅该设备的浏览器更新状态。

### 9.3 创建和投递命令

1. 浏览器发送 command.request，携带 idempotency_key 和 lease_id。
2. Control API 校验 RBAC、设备 capability、租约 epoch、payload 和过期时间。
3. 对 REQUIRE_ONLINE 命令，Control API 只读检查 Redis presence；设备离线时返回 REJECTED，Redis 不可用时返回 UNAVAILABLE，不创建命令。
4. Control API 在一个事务中分配 device_sequence，写 commands、audit_records 和 outbox。
5. Dispatcher 消费 outbox，通过 Control API 重新校验命令尚未过期、租约仍有效，并读取当前 Gateway owner。
6. Dispatcher 创建包含当前 target_session_generation 的 CommandAttempt，并把它写入该 Gateway 的命令 Stream。
7. Gateway 校验 target_session_generation 与本地连接匹配，然后写入本地 gRPC 流。
8. Gateway 成功把命令写入设备流后，通过 Control API 记录 DISPATCHED；没有收到应用层 ACK 不等于成功。

### 9.4 设备 ACK

1. 设备先检查 expires_at、投递尝试的 session_generation、lease_epoch 和 device_sequence。
2. 设备使用 command_id 或 idempotency_key 查询本地已执行缓存。
3. 未执行命令调用 adapter；重复命令返回第一次执行结果。
4. 设备发送 CommandAck。
5. Gateway 将 ACK 转发到 Control API。
6. Control API 以条件更新完成状态转换，并写 audit 和 outbox；Dispatcher 随后投影 domain_event。
7. 浏览器收到 command.status；重复 ACK 只增加诊断计数，不改变终态。

### 9.5 WebSocket 重连与事件重放

1. 浏览器保存最后确认处理的 event_cursor。
2. 重连后先完成身份验证和 topic 订阅。
3. 客户端发送 resume 请求和 last_event_cursor。
4. Gateway 通过 Control API 从 PostgreSQL 查询遗漏的持久事件。
5. 回放结束后发送 resume.complete，再切换到 Redis 实时流。
6. 回放窗口内的重复事件由 event_id 去重，顺序由 event_offset 决定。

遥测不保证完整回放。重连时先返回最新 snapshot，然后继续实时更新。

## 10. 一致性与并发规则

### 10.1 每设备顺序

Control API 使用 device_counters 在数据库事务中分配 device_sequence。Dispatcher 对每个设备最多保留一个普通控制命令处于 in-flight；只有前一个命令进入 SUCCEEDED、FAILED、EXPIRED 或 CANCELLED，才会发送下一个命令。

设备保存 last_applied_sequence，拒绝小于或等于该值的迟到命令。序号允许出现间隙，因为未发送命令可能在服务端过期或取消；发送顺序由 Dispatcher 保证。ACK_TIMEOUT 会暂停该设备的后续普通命令，直到查询设备状态、收到迟到 ACK 或由 operator 执行显式对账。只有 capability policy 明确标记的安全命令，例如紧急停止，才可以越过暂停状态。

### 10.2 幂等性

同一 operator、device_id 和 idempotency_key 只能对应一个语义命令。客户端超时后可以安全重试；服务端返回已存在命令及其当前状态。

### 10.3 Fencing

lease_epoch 防止旧操作员继续控制；session_generation 防止旧 Gateway 或旧设备连接继续处理新命令。两个值解决不同竞争，不合并成一个模糊版本号。

### 10.4 时间

租约有效性和命令过期以服务端时间为准。设备本地时钟只用于诊断，不决定授权。服务端返回当前时间，便于观测明显时钟漂移。

### 10.5 离线命令

默认策略是 REQUIRE_ONLINE，避免扫地机器人几小时后突然执行过期的 START_TASK。QUEUE_UNTIL 只允许用于被 schema 明确标注为可排队的命令，并且必须具有 expires_at。

## 11. 背压与资源边界

### 11.1 WebSocket 慢消费者

每个浏览器连接有两个有界结构：

- 可靠事件队列：命令状态、租约变化和告警；队列满时连接以 1013 状态关闭，并返回 resume cursor。
- 遥测最新值缓存：相同 device/topic 只保留最新值，旧值被覆盖。

单个慢客户端不能阻塞 Gateway 的 Redis consumer、其他 WebSocket 连接或设备 gRPC 流。

### 11.2 gRPC 设备流

每条设备流使用独立发送队列，并限制最大消息大小、并发命令数和未确认命令数。控制消息优先于非关键服务器通知。持续不读消息的设备会被断开，其未完成命令回到可恢复状态。

### 11.3 遥测入口

Gateway 对每设备和 topic 执行速率限制。超过 capability 声明的遥测速率时，系统合并或丢弃旧样本并增加 telemetry_dropped_total，不让遥测压力扩散到 Control API。

## 12. 故障语义

| 故障 | 系统行为 | 恢复方式 | 用户可见状态 |
|---|---|---|---|
| Gateway 崩溃 | 本地 gRPC/WS 连接断开；PostgreSQL 状态不变 | 设备和浏览器抖动重连；新 session generation 生效 | 设备短暂 OFFLINE/RECONNECTING |
| Redis 中断 | 已接受命令的 outbox 保留；新 REQUIRE_ONLINE 请求返回 UNAVAILABLE；跨实例实时投递暂停 | Dispatcher 重试并从未发布 outbox 继续 | 已接受命令 PENDING_TRANSPORT，实时状态 DEGRADED |
| PostgreSQL 中断 | 不接受新租约或命令；不确认状态转换 | 数据库恢复后客户端幂等重试 | API UNAVAILABLE，不假报成功 |
| ACK 丢失 | 命令可能已执行但状态未确认 | 重发相同 command_id；设备返回缓存结果；重试窗口结束仍无结果则 ACK_TIMEOUT | DISPATCHED，随后 SUCCEEDED、FAILED 或 ACK_TIMEOUT |
| 重复命令 | 设备识别已执行命令 | 返回原执行结果 | 状态不产生第二次副作用 |
| 操作员断线 | 租约不立即转移 | TTL 到期或显式释放；新 epoch 发放 | 控制权显示即将过期 |
| 设备半开连接 | presence TTL 到期 | Gateway 关闭流；设备重连 | OFFLINE |
| 旧 Gateway 迟到发送 | generation 不匹配 | 设备拒绝并记录 stale_session | 不执行命令 |
| 旧操作员命令迟到 | lease_epoch 过旧 | 服务端或设备拒绝 | STALE_LEASE |
| 慢 WebSocket 客户端 | 队列达到上限 | 1013 关闭，客户端带 cursor 重连 | 提示正在恢复事件 |

Redis 故障期间已经持久化的命令不会丢失，但 V1 不保证实时投递仍然可用。PostgreSQL 故障期间系统 fail closed，因为授权、租约和命令状态无法可靠判断。

## 13. 安全设计

### 13.1 身份认证

- 设备连接使用开发环境本地 CA 签发的 mTLS 证书。
- 证书 SAN 中的设备身份必须与 DeviceHello 一致。
- 浏览器使用短期 JWT；WebSocket upgrade 时验证 token、origin 和 audience。
- 服务间 gRPC 在 Docker 网络中仍使用独立 service identity；生产化扩展可以替换为外部身份系统。

### 13.2 权限

V1 提供三种角色：

- viewer：查看设备、遥测、告警和历史。
- operator：获取租约并发送设备支持的控制命令。
- admin：注册设备、管理能力和撤销租约。

权限检查只在 Control API 中做权威决定，Gateway 的早期检查只能用于快速拒绝，不能替代领域授权。

### 13.3 输入与滥用防护

- Protobuf 使用 Protovalidate 约束字段。
- WebSocket JSON 使用明确 schema_version、消息类型白名单和最大消息大小。
- 按 operator 和 device 进行命令速率限制。
- 敏感字段不进入普通日志；审计事件保留 actor 和结果，不保存令牌。
- WebSocket 只接受配置允许的 Origin。
- 服务暴露独立的 liveness 与 readiness；依赖降级不会被错误报告为完全健康。

## 14. 可观测性

### 14.1 Trace

一次命令的 trace 覆盖：

WebSocket request → Gateway → Control API transaction → outbox → Dispatcher → Redis Stream → Gateway → gRPC device → ACK → Control API → WebSocket event。

所有 span 关联 command_id、device_id、lease_epoch 和 session_generation，但不记录完整敏感 payload。

gRPC 使用 OpenTelemetry otelgrpc stats handler，HTTP/WebSocket upgrade 使用 otelhttp 或显式 span。不会使用已经弃用的 gRPC tracing interceptor 模式。

### 14.2 Metrics

核心指标包括：

- connected_devices 与 connected_operators。
- grpc_stream_duration_seconds。
- websocket_outbound_queue_depth。
- telemetry_received_total 与 telemetry_dropped_total。
- command_end_to_end_seconds。
- commands_by_status_total。
- command_delivery_attempts_total。
- stale_lease_rejections_total。
- stale_session_rejections_total。
- outbox_oldest_unpublished_seconds。
- redis_consumer_lag。
- device_reconnect_seconds。

### 14.3 Logs 与 Dashboard

使用 Go slog 输出 JSON 日志。日志字段与 trace context 对齐。Grafana 至少提供 Fleet Overview、Command Delivery、Connection Health 和 Failure Injection 四个 dashboard。

## 15. 扫地机器人参考应用

### 15.1 家庭模型

演示家庭有三层，每层一台逻辑扫地机器人：

- vacuum-floor-1：正常清扫。
- vacuum-floor-2：可触发 STUCK 告警并接受人工控制。
- vacuum-floor-3：可模拟离线、低电量和充电座不可用。

两个浏览器操作员可以同时查看三台设备，但只能有一个人控制某台机器人。

### 15.2 通用命令映射

| FleetRelay 命令 | Vacuum Bridge 行为 |
|---|---|
| START_TASK | 开始指定区域或预设任务 |
| PAUSE_TASK | 暂停当前清扫 |
| RESUME_TASK | 恢复清扫 |
| RETURN_HOME | 返回充电座 |
| CANCEL_TASK | 取消当前任务 |
| QUERY_STATE | 返回电量、状态和错误码 |

具体设备不支持的命令返回 UNSUPPORTED。Adapter 不把厂商错误伪装成成功，也不向核心系统泄漏不可移植的厂商结构。

### 15.3 Vacuum Bridge 接口

~~~go
type VendorDriver interface {
    Capabilities(ctx context.Context) (Capabilities, error)
    Execute(ctx context.Context, command Command) (Result, error)
    Snapshot(ctx context.Context) (TelemetrySnapshot, error)
    Events(ctx context.Context) (<-chan DeviceEvent, error)
}
~~~

V1 必须完成确定性 Vacuum Simulator Driver。真实硬件 Driver 在确认用户拥有的品牌、型号和合法可用的官方或本地 API 后实现；硬件集成结果与模拟器结果分开记录。

### 15.4 家庭演示流程

1. 启动三个模拟机器人和两个 Gateway。
2. 浏览器显示每层设备、电量和任务状态。
3. 二楼机器人产生 STUCK 告警。
4. Operator A 获得租约并发送 PAUSE_TASK。
5. Operator B 尝试控制同一设备并收到 CONFLICT。
6. kill 持有设备连接的 Gateway。
7. 机器人重连另一个 Gateway；浏览器通过 cursor 恢复事件。
8. Operator A 重新获得新租约，发送 RETURN_HOME。
9. 重放重复命令，机器人只执行一次并返回相同结果。
10. 审计页面展示完整命令、租约和故障恢复链路。

## 16. 技术选型与 GitHub 趋势核验

### 16.1 采用

| 技术 | 用途 | 选择原因 |
|---|---|---|
| Go | 全部后端与模拟器 | 并发、网络服务、部署简单，且与目标岗位匹配 |
| grpc-go | 设备双向流和内部 RPC | 明确展示标准 gRPC/HTTP2 能力 |
| Protocol Buffers Go | 强类型消息 | 适合设备与服务间契约 |
| Buf | schema 构建、lint、breaking、generate | 减少手写 protoc 脚本并提供兼容性门禁 |
| Protovalidate Go | Protobuf 约束 | 把验证规则靠近协议定义 |
| coder/websocket | 浏览器实时连接 | API 简洁、context 支持、一致关闭握手和测试覆盖 |
| PostgreSQL | 权威状态、审计、outbox | 事务与约束适合控制正确性 |
| Redis Streams | 跨实例实时路由 | 支持消费组和短期重放，复杂度低于引入新平台 |
| OpenTelemetry Go | trace、metrics 上下文 | 贯穿 WebSocket、gRPC、数据库和 dispatcher |
| Prometheus/Grafana | 指标与演示 | 可本地复现并适合故障仪表盘 |
| Docker Compose | 本地部署 | 一条命令运行完整依赖和多实例场景 |

依赖版本在实施计划中固定，并由自动更新工具以独立 PR 升级。设计文档不把某个瞬时版本号或 GitHub star 数当作架构理由。

### 16.2 未采用或延后

- gorilla/websocket：成熟且普及，可作为替代；V1 更偏好 coder/websocket 的 context-first API。
- Centrifuge：功能丰富，但会隐藏本项目最需要展示的连接管理、背压和重放实现。
- gobwas/ws 或事件循环框架：适合极限优化，V1 没有证据表明需要更低层 API。
- ConnectRPC：优秀的 Protobuf HTTP 方案，但 V1 要明确展示 grpc-go 双向流，因此不作为核心传输。
- GraphQL：查询复杂度不足以抵消额外 schema 和 resolver 成本。
- Socket.IO：浏览器兼容和 fallback 不是主要约束，也不希望把 Go 服务绑定到 Socket.IO 语义。
- SSE：适合单向事件推送，但 FleetRelay 的操作员连接需要订阅、租约续期和命令请求等双向交互。
- Kafka 或 NATS JetStream：未来规模或跨区域要求可能证明其价值，V1 先避免第二套消息平台。

## 17. 代码仓库结构

~~~text
fleetrelay/
├── api/
│   └── fleetrelay/v1/          # Protobuf contracts
├── cmd/
│   ├── control-api/
│   ├── stream-gateway/
│   ├── dispatcher/
│   ├── device-sim/
│   └── vacuum-bridge/
├── internal/
│   ├── auth/
│   ├── command/
│   ├── device/
│   ├── lease/
│   ├── eventlog/
│   ├── outbox/
│   ├── relay/
│   ├── telemetry/
│   └── vacuum/
├── migrations/
├── web/                        # Minimal React/TypeScript console
├── deploy/
│   ├── compose/
│   └── observability/
├── test/
│   ├── integration/
│   ├── e2e/
│   ├── chaos/
│   └── load/
├── docs/
│   ├── adr/
│   ├── demo/
│   ├── interview/
│   └── evidence/
└── Makefile
~~~

生成代码与领域代码分离。每个 internal package 只拥有一个领域职责，并通过接口或生成的 Protobuf 合约通信。

## 18. 测试策略

### 18.1 单元与属性测试

- 命令状态机只允许合法转换。
- 租约 epoch 和 session generation 单调增加。
- 重复 command_id、idempotency_key 和 ACK 保持幂等。
- 过期命令永远不能进入执行状态。
- 旧 lease_epoch 和旧 generation 必须被拒绝。
- 遥测 coalescer 永远保留最新值且不会无界增长。
- WebSocket 优先级队列保持控制事件顺序。

所有并发包运行 go test -race。状态机使用生成的事件序列进行属性测试，而不只覆盖少数手写 happy path。

### 18.2 Contract 测试

- buf build、buf lint 和 buf breaking 对主分支执行。
- 生成代码必须与 schema 同步，CI 检查工作树无差异。
- Protovalidate 正反例测试覆盖每个外部消息。
- Vacuum Simulator 和 Vacuum Bridge 运行同一 adapter contract suite。

### 18.3 集成测试

使用真实 PostgreSQL 和 Redis 容器验证：

- 命令与 outbox 原子提交。
- Dispatcher 重启后从未发布 outbox 恢复。
- Redis 重复投递不会改变命令终态。
- 两个 Control API 实例竞争租约时只有一个成功。
- 两个 Gateway 之间能够路由设备命令和浏览器事件。

### 18.4 端到端测试

从真实 WebSocket 客户端经过 Control API、Dispatcher、Redis 和 gRPC 模拟设备，再把 ACK 推回浏览器。端到端测试不得直接调用内部状态修改函数绕过传输层。

### 18.5 故障注入

- kill 当前设备所在 Gateway。
- Redis 停止 30 秒后恢复。
- PostgreSQL 暂时不可用。
- 丢弃第一次 ACK。
- 重复发送同一命令和 ACK。
- 让旧 Gateway 延迟发送命令。
- 让一个 WebSocket 客户端停止读取。
- 让设备心跳停止但 TCP 暂时保持。

每种故障都必须定义预期状态、恢复条件和机器可读结果。

## 19. 基准与验收门槛

### 19.1 标准本地负载配置

每次正式基准记录 CPU、内存、操作系统、Go 版本、容器版本、Git SHA 和完整参数。标准场景为：

- 两个 Stream Gateway 实例。
- 1,000 个并发 gRPC 设备流。
- 每个设备每 5 秒 heartbeat、每秒 1 条 telemetry。
- 250 个 WebSocket 操作员连接。
- 每个操作员订阅 20 台设备。
- 稳态 50 commands/second，持续 10 分钟。
- 命令 adapter 延迟使用固定、可配置的确定性分布。

### 19.2 V1 设计门槛

以下是待实现系统必须通过的验收门槛，不是当前实测结果：

- 标准本地负载下，已在线设备的 command ACK p95 不超过 500 ms；报告 p50、p95、p99 和最大值。
- 10,000 次包含重试和重复投递的确定性命令执行中，模拟器记录的重复业务副作用为 0。
- kill 一个 Gateway 后，95% 设备在 15 秒内恢复连接，全部设备在 30 秒内恢复或返回明确失败原因。
- Redis 中断 30 秒期间，已接受命令丢失数为 0；恢复后 60 秒内清空测试产生的 outbox backlog。
- 慢 WebSocket 客户端被隔离或断开，不得使其他客户端和设备命令 p95 增加超过基线的 20%。
- 负载结束后，Gateway 的连接队列全部释放；goroutine 和 heap profile 不显示随消息总数线性增长的保留路径。
- go test -race、contract、integration、E2E 和 failure suite 全部通过。

若某项门槛未通过，报告实际结果、环境和瓶颈，不能删除失败记录或把设计目标写成简历成绩。

## 20. 可演示场景

### Demo A：正常控制链路

浏览器获取租约，向在线设备发送命令，观察从 ACCEPTED 到 SUCCEEDED 的 trace、指标和审计记录。

### Demo B：并发控制竞争

两个操作员同时申请同一设备租约，只有一个成功；旧控制者的迟到命令被 lease_epoch 拒绝。

### Demo C：Gateway 故障转移

设备连接 Gateway A，命令处理中 kill A。设备重连 Gateway B，旧 generation 被隔离，未完成命令恢复且不重复执行。

### Demo D：Redis 降级

先接受命令，再在 Dispatcher 发布前停止 Redis；命令保持 PENDING_TRANSPORT，PostgreSQL 状态完整。新的 REQUIRE_ONLINE 请求明确返回 UNAVAILABLE；恢复 Redis 后 Dispatcher 继续处理 outbox。

### Demo E：家庭多楼层机器人

三层楼机器人同时上报状态；二楼设备卡住后由一个操作员接管，另一个操作员被租约拒绝；Gateway 故障后设备重连并安全返回充电座。

## 21. ATS 与面试呈现

### 21.1 ATS 关键词

在真实实现和验证完成后，项目自然覆盖：

Go, gRPC, Protocol Buffers, bidirectional streaming, WebSocket, REST API, PostgreSQL, Redis Streams, transactional outbox, idempotency, distributed systems, concurrency, backpressure, OpenTelemetry, Prometheus, Grafana, Docker, RBAC, mTLS, load testing, fault injection, observability。

### 21.2 面试主线

推荐用一个问题贯穿项目：

> ACK 丢失时，系统如何判断命令是否执行，并保证安全重试？

由此可以自然解释数据库权威状态、transactional outbox、at-least-once、设备端幂等、command_id、ACK 重放、租约 fencing 和审计证据。

其他可准备的设计故事：

- 为什么命令必须持久化，而遥测可以 coalesce。
- 为什么需要 lease_epoch 和 session_generation 两个 token。
- Redis 故障时为什么系统降级而不是假装实时可用。
- 为什么浏览器使用 WebSocket JSON，而设备使用 gRPC/Protobuf。
- 为什么 V1 使用三个服务，而不是单体或十几个微服务。
- 为什么先用确定性模拟器，再接入真实扫地机器人。

### 21.3 简历证据门禁

只有满足以下条件后才能写成完成性简历 bullet：

- 对应代码已经提交，并能通过固定 Git SHA 定位。
- 测试或基准命令可重复执行。
- 指标包含环境、参数、样本量、基线和原始输出。
- 模拟器、真实硬件、本地容器、云部署分别标注。
- 失败、限制和未运行项保留在证据中。

设计目标、估计值和计划中的功能不能写成已完成成果。

## 22. 实施分解边界

本规格可以进入一个实现计划，但计划必须按可验证能力纵向拆分：

1. Protocol Buffers、领域状态机和 PostgreSQL schema。
2. 单实例设备 gRPC 流与确定性模拟器。
3. 命令持久化、ACK 和幂等执行。
4. WebSocket 操作员会话、订阅和控制租约。
5. Transactional outbox、Dispatcher 和 Redis Streams。
6. 多 Gateway 路由、重连和 replay cursor。
7. 安全、可观测性和故障注入。
8. 负载基准与证据包。
9. Vacuum Bridge 和家庭多楼层参考演示。

每一步都应形成可以独立运行和测试的系统增量，不先搭建无法验证的完整基础设施外壳。

## 23. 风险与控制

| 风险 | 影响 | 控制方式 |
|---|---|---|
| 为了 ATS 堆砌组件 | 项目无法完成或解释 | 坚持三个服务和一套消息平台 |
| 模拟器结果被误写成真实设备结果 | 简历可信度受损 | 证据文件标注 simulator 或 hardware |
| Redis 被误用为权威状态 | 故障时命令状态不一致 | PostgreSQL 事务、outbox 和恢复测试 |
| WebSocket 慢客户端拖垮服务 | 内存增长、延迟扩散 | 有界队列、遥测合并、1013 重连 |
| 旧租约或旧连接发送迟到命令 | 设备执行未授权操作 | lease_epoch 与 session_generation 双 fencing |
| 厂商 API 不可用 | 家庭硬件集成停滞 | 核心协议和模拟器独立；先确认合法 API |
| 过早引入 Kubernetes | 时间花在 YAML 而非系统正确性 | Docker Compose 作为 V1 必须交付 |

## 24. 完成定义

FleetRelay V1 只有在以下条件全部满足时才算完成：

- 三个 Go 服务、设备模拟器和 Web 控制台可以通过 Docker Compose 启动。
- 设备 gRPC 双向流、WebSocket 操作和命令 ACK 端到端可运行。
- 租约、fencing、命令状态机、幂等和 outbox 不变量有自动化测试。
- 两个 Gateway 实例和跨实例路由通过集成测试。
- 标准故障演示可重复执行并生成机器可读结果。
- 标准负载配置生成带环境清单的原始指标。
- 家庭多楼层机器人 reference demo 使用相同协议完成。
- 真实硬件适配只有在实际运行后才标记完成。
- README、架构图、运行手册、面试说明和证据索引与代码一致。

## 25. 参考资料

### 官方项目

- [gRPC-Go](https://github.com/grpc/grpc-go)
- [Protocol Buffers](https://github.com/protocolbuffers/protobuf)
- [Buf](https://github.com/bufbuild/buf)
- [Protovalidate Go](https://github.com/bufbuild/protovalidate-go)
- [coder/websocket](https://github.com/coder/websocket)
- [go-grpc-middleware](https://github.com/grpc-ecosystem/go-grpc-middleware)
- [OpenTelemetry Go Contrib](https://github.com/open-telemetry/opentelemetry-go-contrib)
- [go-redis](https://github.com/redis/go-redis)

### 2026 岗位方向样本

- [Applied Intuition Careers](https://www.appliedintuition.com/careers)
- [Applied Intuition Remote Assistance Backend posting](https://jobs.ashbyhq.com/applied/290def1d-ade4-4f96-b106-494b51c7e046)
- [Block Labs Senior Software Engineer Go posting](https://jobs.ashbyhq.com/block-labs/1c412e6e-5617-47f5-b14c-1fb83a55c398)
- [Planera Backend Software Developer posting](https://jobs.ashbyhq.com/planera/8fcbe202-8880-4f74-95e6-6f2f1219950e)
- [E2B Product Engineer posting](https://jobs.ashbyhq.com/e2b/49a87668-9f2a-40dc-bfc3-b262aca62b39)

这些岗位链接只用于记录设计时的技术需求样本，不保证职位长期开放。
