# AGENTS.md

本文件为所有 coding agent（Copilot、Claude Code、Cursor Agent、Codex 等）在处理 sys-mcp 项目代码时提供统一指导。

---

## 项目简介

sys-mcp 是一个分布式 MCP（Model Context Protocol）平台，让 AI 助手能够查询远程物理机的硬件信息和文件系统。项目由四个 Go 二进制组成：

```
sys-mcp-agent   — 部署在每台物理机上，采集数据、执行工具调用
sys-mcp-proxy   — 可选，部署在 IDC 内，聚合多台 agent（支持多级级联）
sys-mcp-center  — 中心服务，对 AI 暴露 MCP SSE 接口，管理所有 agent 连接
sys-mcp-client  — 本地 stdio 桥接，让 AI 助手通过 MCP 协议与 center 通信
```

---

## 目录结构

```
api/
  proto/          — Protobuf 定义（tunnel.proto）
  tunnel/         — 生成的 Go gRPC 代码（不要手动修改）
cmd/
  sys-mcp-agent/  — agent 可执行入口
  sys-mcp-center/ — center 可执行入口
  sys-mcp-client/ — client 可执行入口
  sys-mcp-proxy/  — proxy 可执行入口
deploy/
  config/         — 配置文件示例
  systemd/        — systemd 服务单元文件
docs/
  design/         — 架构设计文档
  testing/        — 测试流程文档
  trouble/        — 故障记录
  usage/          — 用户使用指南
internal/
  pkg/            — 跨服务公共库（stream、tlsconf）
  sys-mcp-agent/  — agent 内部实现
  sys-mcp-center/ — center 内部实现
  sys-mcp-client/ — client 内部实现
  sys-mcp-proxy/  — proxy 内部实现
```

`internal/` 下只能存放服务自身的代码；跨服务共用的工具类放在 `internal/pkg/`；不在 `internal/` 之外建立 `pkg/` 目录。

---

## 开发规范

### 语言与依赖

- Go 版本：见 `go.mod`。
- 构建工具：Taskfile（`Taskfile.yaml`），不使用 Makefile。
- 常用命令：
  ```bash
  task build   # 编译所有二进制到 bin/
  task test    # 运行所有单元测试
  task vet     # 运行 go vet
  ```

### Proto 更新流程

修改 `api/proto/tunnel.proto` 后，必须重新生成代码：

```bash
protoc \
  --go_out=api/tunnel --go_opt=paths=source_relative \
  --go-grpc_out=api/tunnel --go-grpc_opt=paths=source_relative \
  -I api/proto api/proto/tunnel.proto
```

生成的文件（`api/tunnel/*.pb.go`）不要手动修改。

### 配置文件字段

各服务配置的 YAML 字段名以对应 config 包的 struct tag 为准：

- agent：`upstream.address`、`upstream.token`、`security.*`、`tool_timeout_sec`、`hostname`
- center：`listen.http_address`、`listen.grpc_address`、`auth.client_tokens`、`auth.agent_tokens`、`router.request_timeout_sec`
- proxy：`upstream.*`、`listen.grpc_address`、`auth.agent_tokens`、`hostname`
- client：`center.url`、`center.token`

### MCP 工具注册

`mcp.AddTool()` 要求每个工具都必须设置非空的 `InputSchema`，即使该工具没有参数：

```go
InputSchema: &jsonschema.Schema{Type: "object"}
```

遗漏会导致 panic。

### 二进制文件

- 所有编译产物输出到 `bin/` 目录，不提交到 git。
- `.gitignore` 中使用 `/sys-mcp-*`（带 `/` 前缀）只匹配根目录下的文件，避免误忽略 `internal/` 或 `cmd/` 下的源码。
- 不要将二进制文件提交到任何 commit。

---

## 核心触发 Skill

遇到以下任务时，必须优先调用对应 Skill：

| 场景 | Skill |
|------|-------|
| CI / 构建 / 测试失败 | `ci-analyze` |
| 查看 GitHub issue、PR、discussion | `gh-view` |
| 提交代码、生成 commit message | `git-commit` |
| 审查 Go PR 或 review Go 代码 | `pr-go-review` |
| 查询库文档、API 用法 | `documentation-lookup` |

---

## 冲突处理优先级

当规则冲突时，按以下顺序执行：

1. 用户当前明确需求
2. 子目录 AGENTS.md / README / 代码注释约定
3. 本文件规则

---

## 需求澄清与方案确认

- 需求有歧义时，先列出问题清单确认后再开发。
- 涉及架构或重大技术决策时，先给出 2-3 个可选方案及权衡，确认后实施。
- 写代码前先说明思路并等待用户批准。
- 修改超过 3 个文件时，先给出分步计划和风险提示，逐步确认后执行。

---

## 核心行为准则

- 不要未经独立分析就直接同意或实施用户的建议（避免 AI 讨好行为）。
- 禁止用 sleep/delay 做同步等待，使用正确的同步原语（channel、WaitGroup、Mutex 等）。
- 修复 bug 时，若问题可稳定复现，优先先写一个可复现的测试，再修复直到测试通过。
- 没有明确需求时，不要主动删除代码、配置或兼容逻辑。
- 高风险改动（删除数据、改接口行为、改发布配置）必须先确认。

---

## 沟通与输出格式

- 解释和讨论使用中文；代码、命令、标识符保持英文。
- 回复里不要用加粗强调，用标题、列表或缩进区分层级。
- 禁止使用表情符号。
- 先说结论或摘要，再展开；提到代码时带上文件路径。

---

## 参考文档

- 架构设计：`docs/design/overview.md`
- 本地测试流程：`docs/testing/local-e2e-test.md`
- 用户使用指南：`docs/usage/getting-started.md`
- 故障记录：`docs/trouble/`
