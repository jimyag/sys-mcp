# 快速上手指南

本文档介绍如何安装、配置并运行 sys-mcp，让 AI 助手（如 Claude、Cursor 等）能够查询远程物理机的资源信息。

---

## 系统架构概览

```
AI 助手 (Claude / Cursor / ...)
        │
        │  MCP SSE 协议
        ▼
  sys-mcp-client（本地 stdio 桥接）
        │
        │  HTTP + Bearer Token
        ▼
  sys-mcp-center（中心服务）
        │
        │  gRPC 长连接
        ├──────────────────────────────────────────────┐
        ▼                                              ▼
  sys-mcp-agent（直连物理机）          sys-mcp-proxy（IDC 聚合代理）
                                               │
                                               │ gRPC 长连接
                                          sys-mcp-agent（经代理的物理机）
```

- `sys-mcp-agent`：部署在每台物理机上，采集硬件信息、执行文件操作。
- `sys-mcp-proxy`：可选，部署在 IDC 内网入口，聚合同机房的多台 agent，对外只暴露一条连接到 center。支持多级级联。
- `sys-mcp-center`：部署在公网或内网可达的位置，作为 MCP 服务端对外提供工具接口。
- `sys-mcp-client`：运行在用户本地，作为 stdio 桥接，让 AI 助手通过 MCP 协议与 center 通信。

---

## 安装

### 方式一：从源码编译

```bash
git clone https://github.com/jimyag/sys-mcp.git
cd sys-mcp
task build
# 产物输出到 bin/ 目录
```

### 方式二：下载预编译二进制

访问 [Releases 页面](https://github.com/jimyag/sys-mcp/releases) 下载对应平台的二进制文件。

---

## 部署步骤

### 1. 部署 sys-mcp-center

center 是整个系统的核心，应最先启动。

**配置文件** `/etc/sys-mcp/center.yaml`（参考 `deploy/config/center.yaml.example`）：

```yaml
listen:
  http_address: ":8080"   # AI 客户端连接的 HTTP/SSE 端口
  grpc_address: ":9090"   # agent/proxy 连接的 gRPC 端口

auth:
  client_tokens:
    - "your-client-token"  # AI 客户端使用的令牌
  agent_tokens:
    - "your-agent-token"   # agent/proxy 使用的令牌

router:
  request_timeout_sec: 10

logging:
  level: "info"
  format: "json"
```

**启动**：

```bash
sys-mcp-center -config /etc/sys-mcp/center.yaml
```

**systemd 示例**（参考 `deploy/systemd/sys-mcp-center.service`）：

```bash
cp deploy/systemd/sys-mcp-center.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now sys-mcp-center
```

---

### 2. 部署 sys-mcp-agent

在每台需要被监控的物理机上部署。

**配置文件** `/etc/sys-mcp/agent.yaml`（参考 `deploy/config/agent.yaml.example`）：

```yaml
hostname: "web-server-01"   # 必须唯一，用于在 center 中区分不同机器

upstream:
  address: "center.example.com:9090"  # center 的 gRPC 地址
  token: "your-agent-token"           # 与 center auth.agent_tokens 中的值一致

tool_timeout_sec: 25

security:
  max_file_size_mb: 100
  blocked_paths:
    - /proc
    - /sys
    - /dev

logging:
  level: "info"
  format: "json"
```

注意：`hostname` 字段必须为每台机器设置不同的值。若不设置，默认使用操作系统的 hostname。

**启动**：

```bash
sys-mcp-agent -config /etc/sys-mcp/agent.yaml
```

---

### 3. 部署 sys-mcp-proxy（可选）

当某个 IDC 内有大量 agent（几十到几千台），或 agent 无法直连 center 时，部署 proxy 作为聚合层。

**配置文件** `/etc/sys-mcp/proxy.yaml`（参考 `deploy/config/proxy.yaml.example`）：

```yaml
hostname: "proxy-idc-beijing"  # proxy 的唯一名称

listen:
  grpc_address: ":9091"  # 本 IDC 内 agent 连接的地址

upstream:
  address: "center.example.com:9090"  # center 的 gRPC 地址
  token: "your-agent-token"           # 与 center auth.agent_tokens 一致

auth:
  agent_tokens:
    - "idc-agent-token"  # 本 IDC 内 agent 使用的令牌（可与 center 的令牌不同）

logging:
  level: "info"
  format: "json"
```

**IDC 内 agent 的配置**：将 `upstream.address` 改为 proxy 的地址，token 改为 `idc-agent-token`：

```yaml
upstream:
  address: "proxy-idc-beijing:9091"
  token: "idc-agent-token"
```

**proxy 级联**（多级代理）：将下级 proxy 的 `upstream.address` 指向上级 proxy 即可，支持任意层级。

---

### 4. 配置 sys-mcp-client

`sys-mcp-client` 运行在用户本地，作为 AI 助手与 center 之间的 stdio 桥接。

**配置文件** `~/.config/sys-mcp/client.yaml`（文件权限建议设为 600）：

```yaml
center:
  url: "http://center.example.com:8080"
  token: "your-client-token"

logging:
  level: "info"
```

```bash
chmod 600 ~/.config/sys-mcp/client.yaml
```

---

## 与 AI 助手集成

### Claude Desktop

在 Claude Desktop 的 MCP 配置文件（通常为 `~/Library/Application Support/Claude/claude_desktop_config.json`）中添加：

```json
{
  "mcpServers": {
    "sys-mcp": {
      "command": "/path/to/sys-mcp-client",
      "args": ["-config", "/Users/yourname/.config/sys-mcp/client.yaml"]
    }
  }
}
```

重启 Claude Desktop 后，AI 就可以使用 sys-mcp 提供的工具查询物理机资源。

### Cursor

在 Cursor 设置中添加 MCP Server，命令填写：

```
/path/to/sys-mcp-client -config /path/to/client.yaml
```

---

## 可用工具

center 向 AI 暴露以下 8 个工具，所有工具都需要 `target_host` 参数来指定目标机器：

| 工具名 | 说明 |
|--------|------|
| `list_agents` | 列出所有已注册的节点（agent / proxy）及其状态 |
| `get_hardware_info` | 获取目标机器的 CPU、内存、磁盘等硬件信息 |
| `list_directory` | 列出目标机器的目录内容 |
| `read_file` | 读取目标机器的文件内容 |
| `stat_file` | 获取目标机器文件的元数据（大小、权限、修改时间等） |
| `check_path_exists` | 检查目标机器的路径是否存在 |
| `search_files` | 在目标机器上搜索文件 |
| `proxy_local_api` | 代理访问目标机器上的本地 HTTP 服务 |

使用示例（在 AI 对话中）：

```
请帮我查看 web-server-01 的 CPU 和内存信息
请列出 db-server-02 的 /var/log 目录
请检查 app-server-03 的 /etc/hosts 文件内容
```

---

## 安全建议

1. 生产环境中，center 的 HTTP 和 gRPC 端口应启用 TLS。
2. 令牌（token）应使用足够长的随机字符串（建议 32 位以上）。
3. `client.yaml` 包含令牌，权限应设为 `600`。
4. `security.blocked_paths` 中应包含所有敏感系统目录（`/proc`、`/sys`、`/dev`、`/root` 等）。
5. 如果不需要 `proxy_local_api` 工具，可通过 `security.allowed_ports` 限制可访问的端口。

---

## 常见问题

**Q：如何确认 agent 已成功注册？**

调用 `list_agents` 工具，或直接向 center 查询：

```bash
curl -H "Authorization: Bearer your-client-token" \
     http://center.example.com:8080/sse
```

**Q：agent 断线后会自动重连吗？**

会。agent 内置指数退避重连机制，最大重连间隔由 `reconnect_max_delay_sec` 控制（默认 5 秒）。

**Q：proxy 支持几级级联？**

没有硬限制。实际部署中两到三级通常已经足够：`agent → proxy(IDC) → proxy(Region) → center`。

**Q：center 重启后 agent 需要重启吗？**

不需要。agent/proxy 会自动重连，重连后重新执行注册流程。
