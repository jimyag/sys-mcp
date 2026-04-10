# sys-mcp 架构设计 Review

## 范围说明

本 review 基于当前仓库中的设计文档：

- `docs/design/overview.md`
- `docs/design/sys-mcp-center.md`
- `docs/design/sys-mcp-proxy.md`
- `docs/design/sys-mcp-agent.md`
- `docs/design/sys-mcp-client.md`

当前仓库尚无实现代码，因此本文审查的是设计本身的合理性、完整性与可落地性，不代表实际运行结果。

---

## 结论概览

整体方向是合理的：

- 组件职责大体清晰，`agent / proxy / center / client` 的拆分克制
- 数据面主动建连、默认只读、MCP 与内部隧道解耦，这几个原则是对的
- 以 center 作为唯一 MCP 入口，也有利于后续做统一鉴权、审计和治理

但当前设计仍偏“文档级可讲通”，距离“生产可运行”还有几处关键缺口。主要问题集中在：

1. HA 所有权与路由一致性不够严谨
2. proxy 与 agent 的角色建模混杂
3. 超时与执行预算互相冲突
4. 批量协议与背压机制不完整
5. 安全与审计模型还缺少关键约束

---

## P0 问题

### 1. center 的 owner 模型不够稳，存在 split-brain 风险

当前设计依赖以下组合来实现高可用：

- PostgreSQL 中记录 `owner_center_id`
- 本地内存保存 `RouteStream`
- 重复注册时采用 Last-Write-Wins
- 新 owner 异步通知旧 owner 关闭 stream

这个模型在 happy path 下能工作，但在真实故障下不够稳：

- 旧 stream 未及时关闭时，两个 center 可能都认为自己能处理该 agent
- PostgreSQL 更新、stream 断开、心跳超时三者之间没有统一代际语义
- 路由查到的 `owner_center_id` 可能已经落后于真实连接状态

直接结果是：

- 请求可能被转发到错误 center
- center 可能对已失效 stream 继续发请求
- 注册抢占时可能出现短时间双写或错路由

相关位置：

- [overview.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/overview.md#L105)
- [overview.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/overview.md#L164)
- [sys-mcp-center.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/sys-mcp-center.md#L159)
- [sys-mcp-center.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/sys-mcp-center.md#L317)

建议：

- 给每条连接引入 `stream_id` 或 `generation`
- 所有心跳、响应、路由决策都绑定当前 generation
- center 只接受“当前 owner 且 generation 匹配”的响应
- 把 owner 设计成 lease/fencing 模型，而不是仅靠 Last-Write-Wins

### 2. proxy 和 agent 的建模混在一起，角色边界不干净

proxy 当前被描述为“透明转发层”，但启动后又会把自己作为 `RegisterRequest` 注册到上游。与此同时：

- `agents` 表没有 `node_type`
- `AgentRecord` 没有区分 proxy / agent
- `list_agents` 的输出语义仍是 agent 列表
- 所有 agent 工具依赖 `target_hosts`

这会带来几个问题：

- center 无法从模型层判断某个 hostname 是否可执行工具
- proxy hostname 有机会出现在 agent 列表中，污染调用面
- 后续如果要对 proxy 做单独观测、拓扑展示、路由调试，模型不够用

相关位置：

- [sys-mcp-proxy.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/sys-mcp-proxy.md#L232)
- [overview.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/overview.md#L141)
- [README.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/README.md#L177)

建议：

- 至少给注册表增加 `node_type=agent|proxy`
- 更稳妥的做法是拆分 `agents` 与 `proxy_instances`
- `list_agents` 只返回可执行工具的终端节点
- 如需展示拓扑，单独提供 `list_topology` 或 `list_nodes`

### 3. 请求超时与执行时间预算设计互相冲突

当前文档里：

- center 默认超时 5 秒
- agent handler 允许执行 25 秒，并声称“留 5 秒给网络传输”
- `search_file_content` 又给出了 1GB 文件搜索目标

这三者无法同时成立。

如果 center 5 秒就超时：

- agent 侧大量正常执行会被 center 误判为失败
- proxy pending 会积压
- AI 看到的结果会高度不稳定

相关位置：

- [sys-mcp-center.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/sys-mcp-center.md#L337)
- [overview.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/overview.md#L467)
- [sys-mcp-agent.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/sys-mcp-agent.md#L242)
- [sys-mcp-agent.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/sys-mcp-agent.md#L278)

建议：

- 统一定义端到端 deadline，而不是每层各自估算
- 超时按工具分类，不要全局固定 5 秒
- 至少区分“轻量查询”和“重型文件扫描”
- center 超时后要定义取消语义，确保下游任务能尽快停止

### 4. 批量注册协议不完整，无法表达部分成功

现在 proto 里有：

- `BatchRegisterRequest`

但没有：

- `BatchRegisterAck`
- 单条注册结果
- 部分成功时的重试语义

而 proxy 文档又要求批量注册后“等待 ACK 并批量回复”。这说明协议层和流程层还没对齐。

相关位置：

- [overview.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/overview.md#L241)
- [overview.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/overview.md#L278)
- [sys-mcp-proxy.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/sys-mcp-proxy.md#L183)

建议：

- 明确增加 `BatchRegisterAck`
- 返回每个 hostname 的注册结果
- 约定幂等键和失败重试规则
- 明确 center 对 batch 的原子性要求，是“全成全败”还是“逐项处理”

---

## P1 问题

### 5. 多机并发模型缺少背压、配额和结果收敛约束

`SendMulti` 当前按 host 开 goroutine，再按 owner center 并发转发，方向对，但少了运行时治理：

- 没有单请求最大 `target_hosts`
- 没有每个 token 的并发上限
- 没有 center 本地 worker pool
- 没有单 agent 并发限制
- 没有响应体大小限制

此外，示例代码里远端分支在 `results` 已返回时，如果 `err != nil` 还会对同一批 host 再追加 error，存在重复结果风险。这说明“部分成功 + 聚合失败”的模型还没完全想清楚。

相关位置：

- [sys-mcp-center.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/sys-mcp-center.md#L394)
- [sys-mcp-center.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/sys-mcp-center.md#L457)

建议：

- 给 `SendMulti` 增加显式并发上限
- 限制 `target_hosts` 数量
- 定义聚合语义，保证每个 host 最多返回一条结果
- 给 center 增加 per-token 和全局限流

### 6. 安全模型缺少“身份绑定”这一层

当前设计有：

- gRPC 侧 mTLS
- 注册 token
- HTTP 侧 Bearer Token

但没有定义：

- 证书身份与注册 hostname 如何绑定
- proxy 证书如何和 proxy hostname 绑定
- center 内部转发的实例身份如何校验

如果没有身份绑定，仅靠“合法证书 + 合法 token”还不够，节点仍可能冒用别的 hostname 抢占注册。

相关位置：

- [overview.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/overview.md#L387)
- [sys-mcp-agent.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/sys-mcp-agent.md#L359)

建议：

- 校验证书 SAN/CN 与配置中的节点身份一致
- 注册时 hostname 不应完全信任自报值
- center 内部 gRPC 也要做双向身份校验，而不是仅“内网可达”

### 7. 文件访问控制还不够严，路径模型偏理想化

当前 `PathGuard` 的设计核心是：

- `filepath.Clean(filepath.Abs(path))`
- 前缀匹配白名单/黑名单

这不足以覆盖真实文件系统风险：

- symlink 逃逸
- bind mount / mount boundary
- hardlink 间接访问
- Windows 大小写和卷标差异

相关位置：

- [sys-mcp-agent.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/sys-mcp-agent.md#L199)

建议：

- 使用真实路径解析后再做授权判断
- 明确符号链接策略，是拒绝、跟随，还是仅允许白名单内链接
- 不要只用字符串前缀做最终判定

### 8. client 设计过于乐观，缺少版本与能力协商策略

当前 client 启动时拉一次工具列表，然后本地透明注册。这个设计很轻，但也比较脆：

- center 工具集变化后，client 是否需要重连刷新，没有定义
- center / client 使用的 MCP SDK 版本差异如何处理，没有定义
- transport 失败时本地 stdio server 的可见错误形态也比较粗

相关位置：

- [sys-mcp-client.md](/Users/jimyag/src/github/jimyag/sys-mcp/docs/design/sys-mcp-client.md#L123)

建议：

- 明确工具列表刷新策略
- 定义 client 与 center 的最低兼容版本
- 对初始化失败、鉴权失败、中心不可达做一致错误呈现

---

## 缺失内容

### 1. 缺少 migration 与 schema versioning 设计

当前 DDL 直接写在文档里，但没有说明：

- 如何做 schema migration
- center 多实例滚动升级时如何保持兼容
- 新旧版本字段不一致时如何处理

这对 HA 系统是基础能力，不应后补。

### 2. 缺少取消语义

文档里大量使用 `context`，但没有定义：

- center 超时后是否向 proxy/agent 发取消
- proxy 如何清理 pending
- agent 中长时间文件扫描如何及时停掉

如果没有明确取消协议，超时只会变成“调用方超时了，但下游还在继续跑”。

### 3. 缺少降级策略

例如：

- PostgreSQL 不可用时，center 是否还能服务已连接 agent
- center 内部转发失败时，是否允许回退重查
- proxy 与上游断连期间，是否接受新注册、如何对外呈现状态

这些决定系统故障时的可用性上限。

### 4. 缺少审计与治理模型

系统本质上在做“AI 驱动的远程读操作平台”，至少应明确：

- 谁调用了哪个工具
- 目标 host 是什么
- 访问了哪个路径 / 哪个端口
- 请求是否成功、耗时多久、返回体大小多大
- 是否需要对敏感路径做审计增强或脱敏

如果没有这层，后续上线会很难过安全审查。

### 5. 缺少容量规划与上限

文档提到“支持数万台物理机”，但没有配套说明：

- 单 center 最大连接数
- 单 proxy 扇出能力
- PostgreSQL QPS 预估
- 心跳写入规模
- `list_agents` 和批量调用在大规模下的分页/分片策略

目前“能支撑数万台”还只是目标，不是被设计支撑的结论。

---

## 建议的修订优先级

### 第一阶段：先把模型收紧

- 明确 agent / proxy / center 的身份与数据模型
- 给 owner 模型补 lease/generation/fencing
- 统一超时与取消语义
- 补全批量注册协议

### 第二阶段：补运行时治理

- 增加并发上限、配额、背压
- 定义大批量查询的行为边界
- 增加降级与故障转移策略

### 第三阶段：补安全与运维闭环

- 证书身份绑定
- 文件真实路径授权策略
- 审计日志模型
- migration / versioning / rollout 策略

---

## 最终判断

这套设计的总体拆分方向是对的，也符合“简单、可维护、单一职责”的原则。但目前还存在若干关键缺口，使它更像一份架构草案，而不是可以直接按图施工的生产级设计。

如果后续要进入实现阶段，建议先修订以下四项再开工：

1. owner/route 一致性模型
2. proxy 与 agent 的角色建模
3. timeout/cancellation 端到端语义
4. 批量协议与并发治理

这四项不先收紧，后续实现会很容易在运行时行为上反复返工。
