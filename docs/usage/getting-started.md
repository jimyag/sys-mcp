# 快速上手指南

本文档介绍如何安装、配置并运行 Sysplane，并通过 HTTP API、内置 WebUI 与独立 CLI 管理远程物理机。

---

## 系统架构概览

```text
Web / Admin / CLI
        │
        │ HTTP + Bearer Token
        ▼
  sysplane-center（控制面）
        │
        │ gRPC 长连接
        ├──────────────────────────────────────────────┐
        ▼                                              ▼
  sysplane-agent（直连物理机）          sysplane-proxy（IDC 聚合代理）
                                               │
                                               │ gRPC 长连接
                                          sysplane-agent（经代理的物理机）
```

- `sysplane-agent`：部署在每台物理机上，采集硬件信息、执行文件操作。
- `sysplane-proxy`：可选，部署在 IDC 内网入口，聚合同机房多台 agent。
- `sysplane-center`：部署在公网或内网可达的位置，对外提供 HTTP API 与 `/web/` WebUI。

---

## 安装

### 从源码编译

```bash
git clone https://github.com/jimyag/sysplane.git
cd sysplane
task build
```

编译完成后会在 `bin/` 下得到：

- `sysplane-center`
- `sysplane-agent`
- `sysplane-proxy`
- `sysplane`

---

## 部署步骤

### 1. 部署 sysplane-center

配置文件：`/etc/sysplane/center.yaml`

```yaml
listen:
  http_address: ":18880"
  grpc_address: ":18890"

auth:
  client_tokens:
    - "your-client-token"
  admin_tokens:
    - "your-admin-token"
  agent_tokens:
    - "your-agent-token"
  proxy_tokens:
    - "your-proxy-token"

router:
  request_timeout_sec: 10

logging:
  level: "info"
  format: "json"
```

启动：

```bash
sysplane-center -config /etc/sysplane/center.yaml
```

### 2. 部署 sysplane-agent

配置文件：`/etc/sysplane/agent.yaml`

```yaml
hostname: "web-server-01"

upstream:
  address: "center.example.com:18890"
  token: "your-agent-token"

tool_timeout_sec: 25

security:
  max_file_size_mb: 100
  blocked_paths:
    - /proc
    - /sys
    - /dev
  allowed_commands:
    - /bin/echo

logging:
  level: "info"
  format: "json"
```

启动：

```bash
sysplane-agent -config /etc/sysplane/agent.yaml
```

说明：

- `security.allowed_commands` 是 command template 实际可执行命令的绝对路径白名单。
- 留空时，agent 会拒绝所有命令执行，只保留文件与系统信息类能力。

### 3. 部署 sysplane-proxy（可选）

配置文件：`/etc/sysplane/proxy.yaml`

```yaml
hostname: "proxy-idc-beijing"

listen:
  grpc_address: ":18892"

upstream:
  address: "center.example.com:18890"
  token: "your-proxy-token"

auth:
  agent_tokens:
    - "idc-agent-token"
  proxy_tokens:
    - "idc-proxy-token"

logging:
  level: "info"
  format: "json"
```

启动：

```bash
sysplane-proxy -config /etc/sysplane/proxy.yaml
```

---

## WebUI

启动 `sysplane-center` 后，可直接在浏览器访问：

```text
http://center.example.com:18880/web/
```

当前 WebUI 支持：

- 使用 `client token` 或 `admin token` 登录
- 浏览节点列表、节点详情和能力
- 执行 `sys:info`、`sys:hardware`、`fs:list`、`fs:read`、`fs:stat`、`fs:write`
- 管理命令模板（创建、更新、启停、调用）
- 查看 Invocation 列表、详情、分节点结果，并取消未完成调用
- 查看审计事件

v1 范围内这套 Admin 后台已经完整；RBAC、OIDC、审批流和动态策略控制台不在本轮实现范围内。

---

## CLI

`sysplane` 会直接调用 `sysplane-center` 的 `/v1/...` API。

全局参数：

- `--server`：center 基地址，默认读取 `SYSPLANE_SERVER` / `SYSPLANE_URL`
- `--token`：Bearer token，默认读取 `SYSPLANE_TOKEN`

常见示例：

```bash
sysplane --server http://center.example.com:18880 --token your-client-token nodes list
sysplane --server http://center.example.com:18880 --token your-client-token nodes get <node-id>
sysplane --server http://center.example.com:18880 --token your-client-token fs read --node <node-id> --path /etc/hostname
sysplane --server http://center.example.com:18880 --token your-admin-token templates list
sysplane --server http://center.example.com:18880 --token your-admin-token commands invoke echo.hello --nodes <node-id>
sysplane --server http://center.example.com:18880 --token your-admin-token invocations list
sysplane --server http://center.example.com:18880 --token your-admin-token audit list
```

说明：

- `commands` 是 `templates` 的别名，便于和设计文档里的命令风格保持一致。
- `templates invoke` / `commands invoke` 可以直接传模板名；CLI 会先解析到模板 ID 再调用 API。
- `fs read` 默认直接输出内容；加 `--json` 会输出完整 JSON 结果。

---

## HTTP API

当前可直接调用的主要接口：

- `GET /v1/nodes`
- `GET /v1/nodes/{id}`
- `GET /v1/nodes/{id}/capabilities`
- `POST /v1/nodes/{id}/actions/fs:list`
- `POST /v1/nodes/{id}/actions/fs:read`
- `POST /v1/nodes/{id}/actions/fs:stat`
- `POST /v1/nodes/{id}/actions/fs:write`
- `POST /v1/nodes/{id}/actions/sys:info`
- `POST /v1/nodes/{id}/actions/sys:hardware`
- `GET /v1/command-templates`
- `POST /v1/command-templates`
- `GET /v1/command-templates/{id}`
- `PATCH /v1/command-templates/{id}`
- `POST /v1/command-templates/{id}:invoke`
- `GET /v1/invocations`
- `POST /v1/invocations`
- `GET /v1/invocations/{id}`
- `GET /v1/invocations/{id}/results`
- `POST /v1/invocations/{id}:cancel`
- `GET /v1/audit/events`
- `GET /v1/audit/events/{id}`

创建模板示例：

```bash
curl -X POST \
  -H 'Authorization: Bearer your-admin-token' \
  -H 'Content-Type: application/json' \
  http://center.example.com:18880/v1/command-templates \
  -d '{
    "name":"echo.hello",
    "description":"Echo a fixed string",
    "risk_level":"readonly",
    "target_os":["linux"],
    "executor":{"type":"process","command":"/bin/echo","args":["hello"]},
    "params_schema":{"type":"object","properties":{},"additionalProperties":false},
    "default_timeout_sec":10,
    "max_timeout_sec":30,
    "max_output_bytes":4096
  }'
```

示例：

```bash
curl -H 'Authorization: Bearer your-client-token' \
  http://center.example.com:18880/v1/nodes
```
