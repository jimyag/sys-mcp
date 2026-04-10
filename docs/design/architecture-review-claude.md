# sys-mcp 架构设计 Review（Claude）

> 审查日期：2026-04-10
> 审查范围：README.md、overview.md、sys-mcp-agent.md、sys-mcp-proxy.md、sys-mcp-center.md、sys-mcp-client.md
> 基于 codex-architecture-review.md 之后的修订版本

---

## 总体评价

经过上一轮 review 后的修订，设计文档质量有明显提升。codex review 提出的多数问题已被吸收：

- HeartbeatAck 补了 agent_hostname
- 新增了 BatchRegisterAck、CancelRequest 消息类型
- SendMulti 增加了并发上限（MaxConcurrency）和 not-found 显式处理
- proxy pending 有了超时清理（context + defer delete）
- NodeType 区分 proxy/agent，list_agents 过滤 proxy
- PostgreSQL 降级策略已定义
- schema migration 选型 goose + advisory lock
- stream_generation 防幽灵路由
- Internal gRPC 启用 mTLS
- agent timeout 改为可配置的 tool_timeout_sec（默认 4s）

当前设计在组件职责、协议定义、数据流方面已基本自洽。以下是剩余的问题和新发现的缺陷。

---

## 优先级总览

```
P0（阻碍功能正确性）
  - stream_generation 在 ToolRequest/ToolResponse proto 中未定义字段
  - ForwardToolResponse 缺 hostname，跨实例转发结果无法映射回具体 host

P1（生产稳定性）
  - list_agents 查 PG 但路由用内存，状态不一致窗口可达 90s
  - proxy 重连补注册的时序窗口，中间状态请求处理未定义
  - center 优雅关闭时不通知已连接 agent/proxy 做快速重连
  - hostname 唯一性假设过强，无冲突检测
  - client 工具列表一次性获取，无动态刷新机制

P2（设计完整性）
  - 跨实例转发不传播剩余 deadline
  - 多级 proxy 的 CancelRequest 传播在 pending 已清理时中断
  - proxy 不验证 agent token，可被滥用做放大攻击
  - PathGuard 仍依赖字符串前缀匹配，未解析 symlink
  - 缺少 MCP Streamable HTTP transport 的支持说明
  - ErrorResponse 与 ToolResponse 的交互语义未明确
```

---

## 一、Proto 与协议层

### 1.1 stream_generation 字段在 ToolRequest/ToolResponse 中缺失（P0）

overview.md 第 224 行描述了 stream_generation 防幽灵路由机制：

> 所有发往 agent 的消息（ToolRequest、CancelRequest）均携带当前 generation；agent 若收到 generation 不匹配的消息直接丢弃

但 tunnel.proto 中 ToolRequest 和 CancelRequest 的定义没有 generation 字段：

```protobuf
message ToolRequest {
  string tool_name      = 1;
  string params_json    = 2;
  string agent_hostname = 3;
  // 无 generation 字段
}
```

proto 定义与文字描述矛盾。如果不在 proto 中加这个字段，generation 机制无法落地。

修复：在 ToolRequest 和 CancelRequest 中增加 `int64 stream_generation = N` 字段。同时 ToolResponse 也需要携带 generation，center 用于校验响应是否来自当前有效 stream。

### 1.2 ForwardToolResponse 缺少 hostname 字段（P0）

跨实例批量转发用的 ForwardToolMultiResponse：

```protobuf
message ForwardToolMultiResponse {
  repeated ForwardToolResponse results = 1;
}
```

而 ForwardToolResponse 定义为：

```protobuf
message ForwardToolResponse {
  string result_json = 1;
  bool   is_error    = 2;
}
```

没有 hostname 字段。center-A 收到 center-B 返回的 ForwardToolMultiResponse 后，无法将每条 result 映射回具体的 host。当请求了 3 台但只返回 2 条结果时，甚至无法判断是哪台失败了。

修复：ForwardToolResponse 增加 `string agent_hostname = 3`。或者将 ForwardToolMultiResponse 改为 `repeated HostResult`（与 ToolMultiResult 复用结构）。

### 1.3 ErrorResponse 与 ToolResponse 的交互语义不明确（P2）

TunnelMessage 的 payload oneof 中同时存在 ToolResponse 和 ErrorResponse。对于工具调用的失败场景：

- agent 工具执行失败：是用 ToolResponse{is_error=true} 还是 ErrorResponse？
- proxy 路由失败（PROXY_AGENT_NOT_FOUND）：是发 ErrorResponse 还是包装为 ToolResponse？
- center 收到后如何统一处理？

如果不明确，实现时 center、proxy、agent 三方各自选择不同的路径，会导致 router.Deliver 的匹配逻辑混乱。

建议：定义清晰的约定，例如 ToolResponse 用于"工具已被执行，结果或执行错误在 result_json 中"，ErrorResponse 用于"消息未到达目标 agent 的链路级错误"。

---

## 二、状态一致性

### 2.1 list_agents 与实际路由能力的不一致窗口（P1）

list_agents 查询 PostgreSQL（`Registry.All()`），返回 status=online 的 agent 列表。但路由是基于内存中的 RouteStream。两者之间存在不一致窗口：

场景 1：agent stream 刚断开（本地内存已清除 RouteStream），但 PG 中 status 仍为 online（等心跳超时检测器在下一个周期更新）。AI 看到 agent 在线，发起调用，center 在内存中找不到 stream，转而查 PG 的 owner_center_id 做跨实例转发 -- 但所有 center 实例都没有这个 stream，请求必然超时。

场景 2：agent 刚注册（PG 已写入），但另一个 center 实例还未刷新本地缓存。该实例收到请求后，查 PG 得 owner 是其他实例，转发过去能成功。这个场景倒是能工作。

核心问题在场景 1。心跳超时 90s 意味着一个已断开的 agent 最长 90s 内仍显示为 online。这段时间内所有对该 agent 的调用都会超时（5s），用户体验很差。

建议：
- stream 断开时立即更新 PG status=offline（当前设计文档第 171 行有提到"stream 断开时 PostgreSQL 中标记 offline"，但 sys-mcp-center.md 的 TunnelServiceImpl.Connect 注释只说了"内存路由表清除"，两边描述不完全一致，需要确认实现时确实同步更新 PG）
- 若 PG 写失败（降级模式），至少在本地内存中做标记，SendMulti 查询时优先检查本地状态
- list_agents 可以考虑同时检查本地内存中该 hostname 是否有活跃 stream，给结果加一个 `route_available` 标记

### 2.2 hostname 唯一性假设过强（P1）

agents 表以 hostname 为唯一键（`UNIQUE` 约束）。在以下场景下会出问题：

- 多个网络隔离的 IDC 中可能存在同名的 hostname（如 `web-01`）
- 容器化部署中 hostname 可能重复
- agent 重装系统后 hostname 不变但 IP 变了（这个 UPSERT 能处理，不算问题）

如果两台不同的物理机有相同 hostname，后注册的会覆盖先注册的（Last-Write-Wins），导致前者静默失联。

建议：
- hostname 前加 IDC 或区域前缀（由 proxy_path 推导或配置指定）
- 或改用 hostname + ip 的组合作为唯一标识
- 至少在注册时检测 hostname 冲突（IP 不同但 hostname 相同时发出告警日志）

---

## 三、时序与容错

### 3.1 proxy 重连补注册的时序窗口（P1）

proxy 重连上游后的流程是：
1. 发送 proxy 自身的 RegisterRequest
2. 发送 BATCH_REGISTER_REQ 补注册所有下游 agent
3. 补注册完成后恢复正常

在步骤 1~2 之间（以及步骤 2 等待 BATCH_REGISTER_ACK 期间），上游可能已经开始向这个 proxy stream 下发 TOOL_REQUEST（因为步骤 1 完成后 proxy stream 已建立）。但此时下游 agent 的注册信息可能还没完成补注册，proxy 本地 Registry 虽然有数据（之前留存的），但上游 center 的 PG 中这些 agent 的 owner_center_id 还没更新。

更严重的情况：如果上游是在另一个 center 实例重启后的新连接，这些 agent 的旧 owner 信息指向已经不存在的 center 实例。

建议：
- proxy 在 BATCH_REGISTER_ACK 完成前不应接受上游的 TOOL_REQUEST（或者返回 PROXY_REGISTERING 临时错误）
- center 在收到 proxy 注册后，等 BATCH_REGISTER 完成前暂不将该 proxy stream 用于路由
- 文档中需要明确这个过渡期的行为

### 3.2 center 优雅关闭时不通知下游快速重连（P1）

center 优雅关闭流程：

```
StopHTTP → DrainRouter → StopGRPC → DeregCenter → Exit
```

StopGRPC 会断开所有 agent/proxy 的 stream。但 agent/proxy 只能通过 stream 断开 + 指数退避重连来发现 center 消失。如果使用 DNS Round-Robin 负载均衡，agent 可能还会重连到同一个正在关闭的 center 实例。

建议：
- center 关闭前通过 stream 向所有下游发送一个 `ShutdownNotice` 消息（带新的可用 center 地址或提示立即重连）
- 或者至少在 GracefulStop 前等待足够时间让 LB 健康检查摘除本实例
- 在 DeregCenter 中将该实例持有的所有 agent status 标记为 offline（触发其他实例在 list_agents 中不返回这些 agent）

### 3.3 跨实例转发不传播剩余 deadline（P2）

center-A 收到 AI 请求后启动 5s 超时定时器。假设查 PG + 网络开销用了 1s，此时 center-A 转发给 center-B 时，ForwardToolRequest 不携带 deadline 信息。center-B 会用自己的完整 5s 超时。

结果：center-A 可能在 5s 超时后返回错误给 AI，而 center-B 仍在等待，agent 仍在执行，最多浪费 4s 的计算资源。

修复：ForwardToolRequest 增加 `int64 deadline_unix_ms` 字段，center-B 取 `min(自身超时, 对端传来的 deadline)` 作为实际超时。

---

## 四、安全

### 4.1 proxy 不验证 agent token，可做放大攻击（P2）

sys-mcp-proxy.md 明确说"不做业务鉴权（agent 的 token 透传给 center 验证）"。这意味着：

- 恶意节点可以连接 proxy，发送大量伪造的 RegisterRequest
- proxy 忠实地写入本地 Registry 并全部转发给 center/上游
- center 逐条验证 token 并拒绝，但 proxy → center 的 stream 已经被大量无效消息占满
- 如果恶意节点用 BATCH_REGISTER_REQ 发送上万条伪造注册，proxy 会一次性转发，center 的注册处理也会被打满

实际影响取决于 gRPC stream 的流控和 center 的注册处理能力。但至少 proxy 应做简单的速率限制。

建议：
- proxy 对单条 stream 的注册频率做限流（如每秒最多 10 条）
- 或者 proxy 也持有 agent_tokens 列表做前置校验
- 至少在 proxy 层限制 BATCH_REGISTER_REQ 的最大条目数

### 4.2 PathGuard 仍依赖字符串前缀匹配，未处理 symlink（P2）

这个问题在 codex review 中已提出，但当前设计文档未做修改。PathGuard 的检查逻辑仍然是：

```
filepath.Clean(filepath.Abs(path)) → 前缀匹配 blocked/allowed
```

攻击向量：在白名单目录 /var/log 内创建一个 symlink `/var/log/escape -> /etc/shadow`，然后请求 `read_file{path: "/var/log/escape"}`。PathGuard 只检查 `/var/log/escape` 的前缀（通过），实际读取的是 `/etc/shadow`。

修复：在 PathGuard.Check() 中使用 `filepath.EvalSymlinks()` 解析真实路径后再做白名单/黑名单检查。代价是多一次系统调用，但对安全关键路径是必要的。

---

## 五、客户端与协议演进

### 5.1 client 工具列表无动态刷新机制（P1）

client 启动时拉一次工具列表，然后静态注册到本地 stdio server。这意味着：

- center 新增工具（如 P1 功能 get_process_list 上线后），client 不会知道
- center 移除或修改工具参数后，client 仍注册旧版工具，AI 调用会失败
- MCP 规范定义了 `notifications/tools/list_changed` 通知，但 client 未使用

对于长时间运行的 AI 会话（如 Cursor 可能几小时不重启），这个问题会在 center 升级时暴露。

建议：
- client 监听 center 的 `tools/list_changed` 通知（如果 MCP SDK 支持）
- 或定期轮询 `tools/list`（如每 5 分钟）
- 检测到变化时重新注册工具到本地 stdio server
- 至少在文档中说明当前的限制和重启 workaround

### 5.2 缺少 MCP Streamable HTTP transport 支持说明（P2）

MCP 规范已将 Streamable HTTP 作为推荐的 HTTP transport（替代此前的 HTTP+SSE 模式）。当前设计文档中所有 HTTP 相关描述都是 "HTTP/SSE"：

- README.md: "MCP HTTP/SSE 直连"
- overview.md: "MCP over HTTP/SSE"
- sys-mcp-client.md: "GET /sse" + "POST /message"

需要确认 `github.com/modelcontextprotocol/go-sdk` 的当前支持状态：
- 如果 go-sdk 已支持 Streamable HTTP，设计应更新为优先使用 Streamable HTTP
- 如果尚未支持，至少在文档中标注后续迁移计划
- client 的 transport 选择逻辑也需要相应调整

---

## 六、跨文档一致性

### 6.1 agent config 缺少 hostname 配置项

agent 在注册时需要发送 hostname：

```go
RegisterMsg: &tunnel.RegisterRequest{
    Hostname: hostname,  // 从哪里来？
```

sys-mcp-agent.md 的 AgentConfig 中没有 hostname 字段，代码示例中用的是未定义的 `hostname` 变量。README.md 的配置示例中也没有这个字段。

可能的设计意图是自动获取（`os.Hostname()`），但文档应明确说明，并提供配置覆盖能力（某些环境下 os.Hostname() 返回的值不理想）。

### 6.2 center 配置中 agent_tokens 与 proxy_tokens 未分离

AuthConfig 定义：

```go
type AuthConfig struct {
    ClientTokens []string `yaml:"client_tokens"`
    AgentTokens  []string `yaml:"agent_tokens"`
}
```

README.md 的配置示例中 agent_tokens 包含了 `agent-secret-token` 和 `proxy-secret-token`。但 agent 和 proxy 使用同一个 token 列表意味着无法区分注册来源是 agent 还是 proxy。如果 token 泄露，也无法只撤销一方。

建议分为 `agent_tokens` 和 `proxy_tokens`，注册时根据 RegisterRequest.node_type 选择对应的 token 列表验证。

### 6.3 proxy 配置缺少 heartbeat_timeout 配置

proxy 作为 TunnelService 服务端接受下游连接，需要检测下游 agent 心跳超时。但 ProxyConfig 中没有 heartbeat_timeout 相关配置。

overview.md 的 ListenerConfig 定义了 HeartbeatTimeout（默认 90s），但 proxy 的配置设计中未暴露这个参数。proxy 的下游心跳超时应该可配置，且默认值应与 center 保持一致。

### 6.4 center 的 offline checker 周期未定义

overview.md 提到"center offline checker 定期将超时的 agent 置为 offline"，但未说明检查周期。这个周期直接影响：

- agent 断线后多久从 list_agents 中消失
- PG 与内存状态的不一致窗口长度
- PG 的查询负载（每次扫描所有 online agent）

建议明确定义周期（如 15s）并在 CenterConfig 中可配置。

---

## 七、缺失内容

### 7.1 缺少容量规划基准

设计文档提到"支持数万台物理机"，但没有提供基准数据或推导过程：

- 单个 center 实例能承载多少条 gRPC stream？（取决于文件描述符限制、内存、goroutine 数量）
- 心跳写入 PG 的 QPS：1 万 agent × 每 30s 一次 = 333 QPS UPDATE，是否在 PG 承受范围内？
- list_agents 在 1 万条记录时的响应时间
- SendMulti 传入 100 个 target_hosts 时的端到端延迟

这些不需要精确测量，但至少需要估算和上限设计。

### 7.2 缺少配置校验规则

各组件的 Config 结构体都有 `validate()` 方法，但文档未列出校验规则。至少以下应该在设计时明确：

- upstream.address 不能为空
- token 不能为空（或明确标注哪些场景允许空 token）
- TLS 三个文件必须同时配置或同时为空
- allowed_directories 和 blocked_directories 不能有交集
- tool_timeout_sec 不能大于上游的 request_timeout_sec
- max_file_size_mb 的合理范围

### 7.3 缺少 Observability 端点规划

overview.md 提到预留 Prometheus /metrics 端点（P1），但未说明：

- 哪些组件暴露 /metrics（只有 center？还是全部？）
- agent 不暴露端口，如何采集指标？（push gateway？通过 stream 上报？）
- 健康检查端点（/healthz、/readyz）的设计
- pprof 端点在生产环境的开关

---

## 建议的修订优先级

### 第一阶段（开工前必须修订）

1. 修复 proto：ToolRequest/CancelRequest/ToolResponse 增加 stream_generation 字段
2. 修复 proto：ForwardToolResponse 增加 agent_hostname 字段
3. 明确 ErrorResponse 与 ToolResponse 的使用边界
4. 确认 stream 断开时同步更新 PG status（overview.md 与 sys-mcp-center.md 描述对齐）

### 第二阶段（实现初期）

5. proxy 重连补注册期间的请求处理策略
6. center 优雅关闭的下游通知
7. hostname 唯一性策略（至少加冲突检测日志）
8. client 工具列表刷新机制
9. PathGuard 增加 symlink 解析

### 第三阶段（生产化前）

10. 跨实例转发传播 deadline
11. proxy 注册速率限制
12. agent_tokens / proxy_tokens 分离
13. 容量规划基准测试
14. Observability 端点设计
15. 评估 MCP Streamable HTTP transport 支持

---

## 与 codex review 的对比

codex-architecture-review.md 中提出的问题，当前状态如下：

| codex review 问题 | 当前状态 |
| --- | --- |
| HeartbeatAck 缺 agent_hostname | 已修复（overview.md） |
| SendMulti 不处理 not found | 已修复（sys-mcp-center.md） |
| SendMulti 无并发上限 | 已修复（MaxConcurrency，默认 128） |
| Proxy pending 无超时清理 | 已修复（context + defer） |
| Proxy 混入 list_agents | 已修复（NodeType + 过滤） |
| PG 不可用时行为未定义 | 已修复（降级策略表） |
| BATCH_REGISTER 缺 ACK | 已修复（BatchRegisterAck） |
| 请求取消不传播 | 已修复（CancelRequest） |
| Agent timeout 矛盾 | 已修复（tool_timeout_sec 默认 4s） |
| Schema 迁移空白 | 已修复（goose + advisory lock） |
| Internal gRPC 无认证 | 已修复（mTLS + 独立证书） |
| Token 撤销不影响现有连接 | 已文档化（已知限制） |
| reconnect_max_delay_sec 不一致 | 已统一为 5s |
| owner 模型 split-brain | 部分修复（stream_generation），但 proto 字段缺失 |
| proxy/agent 角色混杂 | 已修复（NodeType） |
| 超时与执行预算冲突 | 已修复（tool_timeout_sec < request_timeout_sec） |
| 安全身份绑定 | 未修复 |
| 文件 symlink 逃逸 | 未修复 |
| client 版本协商 | 未修复 |
| 容量规划 | 未修复 |
| 审计治理模型 | 部分修复（日志字段规范已定义，但独立审计模型未设计） |

---

## 最终判断

相比上一轮 review，设计已经从"架构草案"进化到了"接近可施工"的状态。大部分协议层和容错设计已经到位。

剩余的 P0 问题（stream_generation 字段缺失、ForwardToolResponse 缺 hostname）属于 proto 定义疏漏，修复成本低但必须在编码前完成。P1 问题中最值得关注的是 list_agents 与路由状态的一致性窗口、proxy 重连过渡期的行为、以及 client 工具列表刷新 -- 这三个会在实际运行时直接影响用户体验。

建议先完成第一阶段的 4 项 proto/文档修订，然后开始实现。第二、三阶段的问题可以在实现过程中逐步补全。
