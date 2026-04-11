# sys-mcp 实现 vs 设计 Review（Claude）

> 审查日期：2026-04-11
> 审查范围：全仓库 Go 实现（所有组件），对照 `overview.md`、各组件设计文档及 proto 定义
> 审查方式：逐文件阅读实现代码，与设计文档逐项比对，所有结论已回到源码验证

---

## Findings

### [P0] HA 跨实例路由根本不工作 — `cmd/sys-mcp-center/main.go:194`

`RouterBridge` 已实例化但被显式丢弃：

```go
_ = routerBridge
```

`mcp/server.go` 中的 `registerAgentProxyTool` 只查本实例内存注册表
（`reg.Lookup(targetHost)`），找不到就返回 "agent not found"，完全不走跨实例转发。

设计文档 4.3 节描述的完整跨实例路由流程（查 PG → 取 center 地址 → 转发）在生产路径上从未被触发。多实例 HA 部署下，AI 请求的 agent 若在另一个 center 实例上，必然返回 "not found"。

建议：在 `registerAgentProxyTool` 的 `rec == nil` 分支调用 `routerBridge.ForwardIfNeeded()`，将结果返回 AI。

---

### [P0] proxy 心跳转发实现与设计相悖 — `internal/sys-mcp-proxy/tunnel/downstream.go:148-163`

设计意图：proxy 收到下游 agent 的 Heartbeat 后，通知 center 刷新该 agent 的 `last_heartbeat`。

实际实现：proxy 向上游发送 **RegisterRequest**（而非 Heartbeat），center 的 `Connect` 读循环收到 RegisterRequest 时走 **Register** 路径（`tunnel_svc.go:119-141`），这会把 `RegisteredAt` 也一并刷新，而不是走 `UpdateHeartbeat`。

两条路径的副作用不同：
- `Register`：覆盖整条记录，`RegisteredAt` 被重置为当前时间，误导监控。
- `UpdateHeartbeat`：只更新 `LastHeartbeat` 和 `Status`，语义正确。

正确做法：proxy 应向上游发送 Heartbeat 消息（`&tunnel.TunnelMessage_Heartbeat{...}`），center 的 read loop 已有 Heartbeat 处理分支但只更新直连 agent（`req.Hostname`，即 proxy 的 hostname），还需要额外将 agent hostname 透传（可通过扩展 Heartbeat 消息或走 RegisterRequest 但只做 UpdateHeartbeat 不覆盖注册时间）。

---

### [P0] CancelRequest 只有 proto 定义，无发送也无接收 — `router.go:62-81` / `agent.go:116-148`

设计文档 7.4 节：center 超时后向 agent 发 `CANCEL_REQUEST`，agent 收到后取消正在执行的 handler。

实际情况：
- `router.go:Send()` 超时时只 return error，不向 stream 发 `CancelRequest`。
- `agent.go:dispatch()` 不处理 `CancelRequest` 消息类型（`switch p := msg.Payload.(type)` 没有这个 case）。

后果：agent 的工具 goroutine 在 center 已返回超时错误后仍继续执行，直到 `ToolTimeoutSec`（默认配置）自然结束。高并发场景下积压的 goroutine 会消耗 CPU 和文件描述符。

---

### [P1] schema 迁移未使用 goose — `internal/sys-mcp-center/store/store.go:43-46`

设计文档 4.7 节：使用 `pressly/goose`，迁移文件存放在 `deploy/migrations/`，多实例并发使用 advisory lock 保证安全。

实际实现：`store.Migrate()` 直接执行内嵌的 `schema.sql`（`CREATE TABLE IF NOT EXISTS`），无版本记录、无回滚能力。`deploy/migrations/` 目录不存在，`goose` 依赖也未在 `go.mod` 中引入。

后果：无法安全做 schema 滚动升级；多实例并发启动执行 DDL 依赖 PG 内置的 DDL 锁，不如 goose advisory lock 精确；无法追踪当前 schema 版本。

---

### [P1] BatchRegisterRequest/Ack 未实现

设计文档 proto 定义了 `BatchRegisterRequest` 和 `BatchRegisterAck`，proxy 重连后用批量注册减少消息往返。

实际情况：
- 实际 `tunnel.proto` 中不含这两个消息类型。
- `downstream.go:ReregisterAll()` 用 for 循环逐条发 `RegisterRequest`，不是批量。

虽然功能等价，但两者在高并发重连时的消息量和 center 处理模式差异显著（批量可做原子处理，逐条可能被中途打断）。如果维持逐条，设计文档应同步更新。

---

### [P1] stream_generation 防幽灵路由未实现

设计文档 4.5 节描述了完整机制：`ToolRequest`/`ToolResponse`/`CancelRequest` 均携带 `stream_generation`，agent 和 center 双向校验。

实际情况：
- `tunnel.proto` 的 `ToolRequest`、`ToolResponse`、`CancelRequest` 均无 `stream_generation` 字段。
- `agent_instances` 表的 `schema.sql` 无 `stream_generation` 列。

没有 generation 机制，split-brain 场景下旧 stream 的延迟响应会被当成有效结果接受，可能导致数据污染（拿到的是另一台 agent 执行的结果）。

---

### [P1] HA 内部通信从 gRPC 降级为 plain HTTP — `internal/sys-mcp-center/ha/forwarder.go`

设计文档 4.4 节定义了 `CenterInternalService` gRPC 服务（含 mTLS）。上一轮 review `architecture-review-claude.md` 还专门提到"Internal gRPC 启用 mTLS"作为已修复项。

实际实现：`ForwardToCenter()` 使用 `http.DefaultClient` 发 HTTP POST 到 `/internal/forward`，无 TLS、无 mTLS、无任何认证。

额外问题：
- `http.DefaultClient` 无 idle connection 超时，长时间运行的 center 集群中会累积泄露的 TCP 连接。
- 转发的 HTTP 请求没有携带调用方的剩余 deadline（设计文档 `architecture-review-claude.md` 也提到这一点，实现时未修复）。

---

### [P1] list_agents 返回 proxy 记录 — `internal/sys-mcp-center/mcp/server.go:87-98`

设计文档 overview.md 第 166 行："`list_agents` 只返回 `node_type='agent'` 的记录，防止 proxy hostname 污染 AI 调用面"。

实际实现：`registerListAgents` 调用 `reg.All()` 返回所有注册记录，包含 proxy，AI 会看到 proxy hostname 并可能尝试对 proxy 执行工具调用（proxy 在 center 的注册表中有条目，调用不会报 not found，但工具执行会走到 proxy 本身，proxy 没有工具 handler，结果不可预期）。

修复一行即可：在 `agents = append(...)` 前加 `if r.NodeType != "agent" { continue }`。

---

### [P1] DB 表名/列名与设计文档不一致

设计文档与实际 schema 对比（`internal/sys-mcp-center/store/schema.sql`）：

```
设计文档                        实际
agents                    →    agent_instances
agents.owner_center_id    →    agent_instances.center_id
center_instances.center_id →   center_instances.instance_id
center_instances.internal_grpc_addr → center_instances.internal_address
idx_agents_node_type（设计有）   →   缺失（实际只有 idx_agent_instances_center_id）
stream_generation 列（设计有）   →   缺失
```

命名分歧不影响功能，但与设计文档对读时容易误解。更重要的是 `stream_generation` 列和 `node_type` 索引的缺失直接影响设计功能的可落地性。

---

### [P1] tool_call_logs 表有实现但从未被调用

`store.go` 定义了 `InsertToolCallLog` 和 `CompleteToolCallLog`，schema 有 `tool_call_logs` 表。

整个代码库中无任何地方调用这两个方法。对工具调用的审计能力事实上为零，但 DB 中有空表、代码中有无用方法，产生误导。

---

### [P2] proxy_path 追加方向与设计描述不一致 — `downstream.go:109`

```go
upstreamPath := append([]string{s.proxyHostname}, req.ProxyPath...)
```

proxy 把自己的 hostname 插在最前面。但设计说"proxy 在透传时**追加**自身 hostname"，"追加"通常指加到末尾。实际语义是"最靠近 agent 的 proxy 排在最前"还是"最靠近 center 的 proxy 排在最前"未明确定义，但当前实现与措辞不一致。`ReregisterAll`（第 204 行）有同样问题，两处行为一致，但与设计描述不符。

如果 proxy_path 的含义是"从 agent 出发经过的 proxy 链路"，则每个 proxy 应 append 到末尾，中心看到的是从 agent 到自己的路径顺序。建议在设计文档中明确定义顺序语义。

---

### [P2] NodeType 枚举值与设计 proto 不一致

设计文档 proto：`NODE_TYPE_AGENT = 0`，`NODE_TYPE_PROXY = 1`。
实际 proto：`NODE_TYPE_UNSPECIFIED = 0`，`NODE_TYPE_AGENT = 1`，`NODE_TYPE_PROXY = 2`。

agent 不显式设置 NodeType 时值为 UNSPECIFIED(0) 不是 AGENT(1)。现有代码 `tunnel_svc.go` 用 `!= NODE_TYPE_PROXY` 判断，UNSPECIFIED 被视为 agent，功能上无影响。但 `agent.go:89` 显式发送 `NodeType_NODE_TYPE_AGENT`，所以实际也不会出现 UNSPECIFIED。这是设计文档的过期描述，不是实现 bug，但文档应同步更新。

---

### [P2] HeartbeatAck 设计有 agent_hostname 字段，实际 proto 无此字段

上一轮 review `architecture-review-claude.md` 标注"HeartbeatAck 补了 agent_hostname（已修复）"。

实际 `tunnel.proto` 中 HeartbeatAck 只有 `timestamp_ms` 字段，无 `agent_hostname`。设计文档描述的"proxy 用 agent_hostname 路由 HeartbeatAck 回正确下游"在实现中无法完成，proxy 目前也不做 HeartbeatAck 路由（heartbeat ack 直接在 center 端回复，不经 proxy 转发）。

---

### [P2] RequestID 格式与设计规范不符 — `internal/pkg/stream/dialer.go:22-24`

设计文档：`<center_node_id>-<unix_ms>-<seq>`，用于日志关联时能快速定位 center 实例。

实际：`<prefix>-<unix_ns>-<counter>`，prefix 是工具名或 "stream"，不含 center 实例 ID，毫秒改为纳秒。跨实例日志关联时无法直接从 request_id 判断来源实例。

---

### [P2] multi-tool 覆盖远不及设计意图

设计文档 7.3 节："所有 agent 工具均只接受 `target_hosts`（数组），不提供单台 `target_host` 参数"。

实际：7 个单机工具均使用 `target_host`（单值），只额外注册了 2 个 `_multi` 变体（`get_hardware_info_multi`、`list_directory_multi`）。其余 5 个工具（`stat_file`、`check_path_exists`、`read_file`、`search_file_content`、`proxy_local_api`）无多机并发版本。设计描述的统一接口语义未落地。

---

## Open Questions

1. `RouterBridge.ForwardIfNeeded` 设计在 `implementation-status.md` 中标注"已完成"，但实际 main.go 中 `_ = routerBridge`，状态文档是否应标注为"已实现未集成"？

2. proxy 心跳用 RegisterRequest 刷新上游而非 Heartbeat，这是有意为之（为了复用 Register 的 UPSERT 路径）还是临时 workaround？如果是 workaround，后续计划是什么？

3. `tool_call_logs` 的写入是否有计划接入，还是已决定不做？若不做，schema 和 store 方法可以清除。

4. `/internal/forward` 端点当前完全信任来源（只依赖内网隔离），在实际部署中 center 实例的内部地址是否真的不可达外网？如果可以加 HMAC 或 mTLS，成本多高？

---

## Summary

实现与设计的核心偏差集中在三个方向：

- HA 能力名存实亡：RouterBridge 未接入主路径，跨实例路由形同虚设；内部通信从设计的 gRPC+mTLS 退化为明文 HTTP，且没有 deadline 传播。

- 协议机制未落地：`stream_generation`、`BatchRegisterRequest`、`CancelRequest` 传播均在设计中有完整定义，但实现要么完全缺失（generation 字段不在 proto），要么只实现了一半（cancel 只有 proto 定义无发送/接收逻辑）。

- 数据可观测性为零：`tool_call_logs` 表有 schema 有接口但无调用点；`list_agents` 混入 proxy 记录；proxy 心跳走 Register 路径导致 `RegisteredAt` 被持续刷新，监控数据失真。

优先修复顺序：`RouterBridge 接入路由主路径` > `list_agents 过滤 proxy` > `CancelRequest 发送与接收` > `proxy 心跳走 Heartbeat 而非 Register` > `HA HTTP 转发加认证/TLS`。
