# Sysplane 设计文档索引

## 文档列表

| 文档 | 说明 |
| ---- | ---- |
| [sysplane-vision.md](sysplane-vision.md) | Sysplane 的目标定位、部署模型和演进方向 |
| [sysplane-security-model.md](sysplane-security-model.md) | token 分域、节点身份、链路与访问控制模型 |
| [sysplane-api-v1.md](sysplane-api-v1.md) | v1 HTTP API 资源模型、动作接口和错误语义 |
| [overview.md](overview.md) | 历史总体设计说明，适合查看 tunnel、registry、router 等内部结构 |
| [sys-mcp-agent.md](sys-mcp-agent.md) | agent 内部实现说明 |
| [sys-mcp-proxy.md](sys-mcp-proxy.md) | proxy 内部实现说明 |
| [sys-mcp-center.md](sys-mcp-center.md) | center 内部实现说明 |
| [codex-architecture-review.md](codex-architecture-review.md) | 历史架构 review 记录 |
| [architecture-review-claude.md](architecture-review-claude.md) | 历史架构 review 记录 |
| [implementation-review-claude.md](implementation-review-claude.md) | 历史实现 review 记录 |
| [implementation-status.md](implementation-status.md) | 历史实现状态记录 |

---

## 当前实现摘要

当前仓库已收敛为 Sysplane 控制面实现，对外保留三类服务和一个独立 CLI：

- `sysplane-agent`：部署在目标物理机，负责采集硬件信息、执行文件系统相关只读操作、代理本地 HTTP 请求
- `sysplane-proxy`：可选聚合层，部署在 IDC 或分区网络入口，负责转发下游 agent 连接
- `sysplane-center`：控制面，提供 HTTP API、嵌入式 WebUI，以及 agent / proxy 注册管理
- `sysplane`：直接调用 HTTP API 的命令行工具

不再维护独立 `sysplane-client` 二进制，也不再保留 MCP 兼容入口。

---

## 当前对外入口

1. HTTP API：`/v1/...`
2. WebUI：`/web/`
3. CLI：`sysplane ...`
4. gRPC 注册入口：agent / proxy 到 center 或 proxy 的长连接

---

## 与旧设计文档的关系

- `sysplane-vision.md`、`sysplane-security-model.md`、`sysplane-api-v1.md` 是当前阶段重构的目标文档。
- `overview.md`、`sys-mcp-agent.md`、`sys-mcp-proxy.md`、`sys-mcp-center.md` 仍可用于理解内部实现，但其中若出现旧命名或旧接入方式，应以当前代码和本索引说明为准。
- review / status 类文档保留为历史记录，不代表当前对外接口承诺。
