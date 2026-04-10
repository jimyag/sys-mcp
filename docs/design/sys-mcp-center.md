# sys-mcp-center 详细设计

## 目录

1. [职责与边界](#一职责与边界)
2. [目录结构](#二目录结构)
3. [配置设计](#三配置设计)
4. [模块设计](#四模块设计)
5. [MCP 工具注册与调用流程](#五-mcp-工具注册与调用流程)
6. [Agent 注册表设计（PostgreSQL + 内存）](#六-agent-注册表设计postgresql--内存)
7. [请求路由](#七请求路由)
8. [多机并发查询](#八多机并发查询)
9. [鉴权设计](#九鉴权设计)
10. [启动与关闭流程](#十启动与关闭流程)
11. [测试策略](#十一测试策略)

---

## 一、职责与边界

sys-mcp-center 是整个系统的控制面，职责：

- 暴露 MCP over HTTP/SSE 接口，供 AI 助手或 sys-mcp-client 调用
- 暴露 gRPC `TunnelService` 接口，接受 agent / proxy 的长连接注册（与 proxy 暴露的接口相同）
- 维护全局 agent 注册表：元数据持久化到 PostgreSQL，路由 stream 保留在内存
- 支持多实例高可用部署（多个 center 实例共享 PostgreSQL，跨实例请求转发）
- 鉴权：Bearer Token（HTTP 侧）+ mTLS + 注册 token（gRPC 侧）
- 路由 MCP 工具调用到目标 agent，超时默认 5s；`SendMulti` 按 owner 分组后并发调用各实例
- 提供 `list_agents`、`get_cluster_overview` 等 center 级工具；所有 agent 工具均接受 `target_hosts` 数组

不在 center 职责范围内：
- 不执行任何本地系统操作（所有系统信息查询必须路由到 agent）
- 不做配置管理、软件分发

---

## 二、目录结构

```
internal/sys-mcp-center/
├── config/
│   └── config.go          # CenterConfig 结构体 + 加载逻辑
├── registry/
│   ├── registry.go        # 全局 agent 注册表（PostgreSQL + 内存双层）
│   └── pg.go              # PostgreSQL 操作封装
├── router/
│   ├── router.go          # 请求路由：本地 stream 直发或跨实例转发
│   └── internal_svc.go    # center 内部 gRPC 服务（跨实例转发用）
└── mcp/
    ├── server.go           # MCP 服务器初始化，工具注册
    ├── tools_center.go     # center 专有工具（list_agents、get_cluster_overview 等）
    ├── tools_agent.go      # agent 工具代理（路由到 agent，所有工具接受 target_hosts）
    └── auth.go             # Bearer Token 中间件
```

---

## 三、配置设计

```go
// internal/sys-mcp-center/config/config.go

package config

type CenterConfig struct {
    InstanceID string         `yaml:"instance_id"`   // center 实例唯一标识，如 "center-01"
    Listen     ListenConfig   `yaml:"listen"`
    Postgres   PostgresConfig `yaml:"postgres"`
    Auth       AuthConfig     `yaml:"auth"`
    Router     RouterConfig   `yaml:"router"`
    Logging    LoggingConfig  `yaml:"logging"`
}

type ListenConfig struct {
    HTTPAddress         string    `yaml:"http_address"`          // MCP over HTTP/SSE，如 "0.0.0.0:8443"
    GRPCAddress         string    `yaml:"grpc_address"`          // 接受 agent/proxy 注册，如 "0.0.0.0:9443"
    InternalGRPCAddress string    `yaml:"internal_grpc_address"` // 实例间转发，如 "0.0.0.0:9444"（仅内网）
    TLS                 TLSConfig `yaml:"tls"`
}

type TLSConfig struct {
    CertFile string `yaml:"cert_file"`
    KeyFile  string `yaml:"key_file"`
    CAFile   string `yaml:"ca_file"`
}

type PostgresConfig struct {
    DSN string `yaml:"dsn"`  // 如 "postgres://user:pass@host:5432/sysmcp?sslmode=require"
}

type AuthConfig struct {
    ClientTokens []string `yaml:"client_tokens"`
    AgentTokens  []string `yaml:"agent_tokens"`
}

type RouterConfig struct {
    RequestTimeoutSec int `yaml:"request_timeout_sec"`  // 默认 5
    MaxConcurrency    int `yaml:"max_concurrency"`       // SendMulti 最大并发 goroutine 数，默认 128
}

type LoggingConfig struct {
    LogRequests bool   `yaml:"log_requests"`
    LogFile     string `yaml:"log_file"`
    Level       string `yaml:"level"`
}
```

配置文件默认路径（按优先级）：
1. `--config` 命令行参数
2. `~/.config/sys-mcp-center/config.yaml`
3. `/etc/sys-mcp-center/config.yaml`

---

## 四、模块设计

### 4.1 Center 主结构体

```go
// internal/sys-mcp-center/tunnel/center.go

type Center struct {
    cfg         *config.CenterConfig
    registry    *registry.Registry
    router      *router.Router
    mcpServer   *mcp.Server
    grpcServer  *grpc.Server    // 对外 gRPC（接受 agent/proxy）
    internalSvr *grpc.Server    // 内部 gRPC（center 间转发）
    tunnelSvc   *TunnelServiceImpl
}

func New(cfg *config.CenterConfig) (*Center, error)

// Run 是 cmd/sys-mcp-center/main.go 唯一调用的入口
func (c *Center) Run(ctx context.Context) error
```

main.go 保持极简：

```go
// cmd/sys-mcp-center/main.go
func main() {
    cfg, err := config.Load()
    if err != nil {
        slog.Error("load config", "err", err)
        os.Exit(1)
    }
    c, err := center.New(cfg)
    if err != nil {
        slog.Error("init center", "err", err)
        os.Exit(1)
    }
    if err := c.Run(context.Background()); err != nil {
        slog.Error("center exited", "err", err)
        os.Exit(1)
    }
}
```

### 4.2 TunnelServiceImpl（gRPC 服务实现）

```go
func (s *TunnelServiceImpl) Connect(stream tunnel.TunnelService_ConnectServer) error {
    // 1. 等待第一条消息（REGISTER_REQ 或 BATCH_REGISTER_REQ）
    // 2. 验证 token
    // 3. 写入 registry（UPSERT PostgreSQL + 写内存路由表）
    //    - 重复注册：Last-Write-Wins，更新 owner_center_id
    //    - 若旧 owner 是其他实例，异步通知旧实例关闭旧 stream
    // 4. 开始循环读取消息（HEARTBEAT / TOOL_RESPONSE）
    // 5. stream 断开时：内存路由表清除，PostgreSQL 中标记 offline（不删除记录）
}
```

消息路由：

```
REGISTER_REQ       → registry.Register(...)        [Last-Write-Wins UPSERT]
BATCH_REGISTER_REQ → registry.RegisterBatch(...)
HEARTBEAT          → registry.UpdateHeartbeat(hostname)
TOOL_RESPONSE      → router.Deliver(requestID, response)
```

---

## 五、MCP 工具注册与调用流程

```mermaid
flowchart TD
    subgraph 启动阶段
        S1[初始化 mcp.Server] --> S2[注册 center 专有工具]
        S2 --> S3[注册 agent 代理工具\n全部接受 target_hosts 数组]
        S3 --> S4[启动 HTTP/SSE transport]
    end

    subgraph 调用阶段
        C1[AI 调用工具] --> C2{center 专有工具?}
        C2 -->|是| C3[直接查 PostgreSQL 返回]
        C2 -->|否| C4[从 params 取 target_hosts\n调用 router.SendMulti]
        C4 --> C5[按 owner_center_id 分组]
        C5 --> C6[本实例 hosts 本地处理\n其他实例 hosts 并发转发]
        C6 --> C7[聚合所有结果]
        C7 --> C8[包装为 ToolMultiResult 返回]
    end
```

### 工具注册示例

```go
func makeAgentProxyHandler(toolName string, r *router.Router) mcp.ToolHandlerFunc {
    return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        hostsRaw, ok := req.Params.Arguments["target_hosts"].([]any)
        if !ok || len(hostsRaw) == 0 {
            return mcp.NewToolResultError("target_hosts is required"), nil
        }
        result, err := r.SendMulti(ctx, toStringSlice(hostsRaw), toolName, req.Params.Arguments)
        if err != nil {
            return mcp.NewToolResultError(err.Error()), nil
        }
        b, _ := json.Marshal(result)
        return mcp.NewToolResultText(string(b)), nil
    }
}
```

---

## 六、Agent 注册表设计（PostgreSQL + 内存）

### 数据分层

| 数据            | 存储位置   | 说明                                                     |
| --------------- | ---------- | -------------------------------------------------------- |
| agent 元数据    | PostgreSQL | 所有字段，含 owner_center_id                             |
| 心跳时间戳      | PostgreSQL | last_heartbeat，offline checker 定期扫描                 |
| center 实例信息 | PostgreSQL | center_instances 表，定期续约，跨实例路由发现用          |
| 路由 stream     | 内存       | 只存在于持有连接的 center 实例                           |

### 数据库 Schema（Go 结构体表示）

```go
// internal/sys-mcp-center/registry/pg.go

// AgentRow 对应 PostgreSQL agents 表（每行有唯一 ID）
type AgentRow struct {
    ID              int64     `db:"id"`               // BIGSERIAL，自增主键
    Hostname        string    `db:"hostname"`          // 唯一索引
    IP              string    `db:"ip"`
    OS              string    `db:"os"`
    AgentVersion    string    `db:"agent_version"`
    RegisteredAt    time.Time `db:"registered_at"`
    LastHeartbeat   time.Time `db:"last_heartbeat"`
    Status          string    `db:"status"`            // "online" / "offline"
    OwnerCenterID   string    `db:"owner_center_id"`
    ProxyPath       []string  `db:"proxy_path"`        // TEXT[]
    NodeType        string    `db:"node_type"`         // "agent" / "proxy"
    StreamGeneration int64    `db:"stream_generation"` // 防幽灵路由，UPSERT 时自增
}

// CenterInstanceRow 对应 center_instances 表
type CenterInstanceRow struct {
    ID                 int64     `db:"id"`
    CenterID           string    `db:"center_id"`         // 唯一索引
    InternalGRPCAddr   string    `db:"internal_grpc_addr"`
    LastHeartbeat      time.Time `db:"last_heartbeat"`
}
```

对应 DDL：

```sql
CREATE TABLE agents (
    id               BIGSERIAL   PRIMARY KEY,
    hostname         TEXT        NOT NULL UNIQUE,
    ip               TEXT        NOT NULL,
    os               TEXT        NOT NULL,
    agent_version    TEXT        NOT NULL,
    registered_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_heartbeat   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status           TEXT        NOT NULL DEFAULT 'online',
    owner_center_id  TEXT        NOT NULL,
    proxy_path       TEXT[]      NOT NULL DEFAULT '{}',
    node_type        TEXT        NOT NULL DEFAULT 'agent',    -- 'agent' / 'proxy'
    stream_generation BIGINT     NOT NULL DEFAULT 0           -- UPSERT 时自增，用于防幽灵路由
);
CREATE INDEX idx_agents_status      ON agents(status);
CREATE INDEX idx_agents_hostname    ON agents(hostname);
CREATE INDEX idx_agents_node_type   ON agents(node_type);

CREATE TABLE center_instances (
    id                  BIGSERIAL   PRIMARY KEY,
    center_id           TEXT        NOT NULL UNIQUE,
    internal_grpc_addr  TEXT        NOT NULL,
    last_heartbeat      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### Registry 接口

```go
// internal/sys-mcp-center/registry/registry.go

type AgentRecord struct {
    AgentRow                        // 嵌入 DB 行字段
    RouteStream   stream.TunnelStream  // 仅内存，不持久化
}

type Registry struct {
    db       *sql.DB
    centerID string
    mu       sync.RWMutex
    local    map[string]*AgentRecord  // hostname → 本实例持有的记录
}

func (r *Registry) Register(row AgentRow, s stream.TunnelStream) error
func (r *Registry) RegisterBatch(rows []AgentRow, s stream.TunnelStream) error
func (r *Registry) Unregister(hostname string)
func (r *Registry) UnregisterByStream(s stream.TunnelStream) []string
func (r *Registry) Lookup(hostname string) (*AgentRecord, bool)
func (r *Registry) All() ([]*AgentRow, error)         // 查 PostgreSQL
func (r *Registry) UpdateHeartbeat(hostname string) error
```

### 重复注册处理

```mermaid
flowchart TD
    A[收到 REGISTER_REQ\nhostname=server-01] --> B{PostgreSQL 中已有记录?}
    B -->|无| C[UPSERT PostgreSQL\nowner=center-1\n写本地内存]
    B -->|owner=本实例| D[UPSERT PostgreSQL\n覆盖旧记录\n更新本地内存 stream]
    B -->|owner=其他实例| E[UPSERT PostgreSQL\nowner 改为 center-1\n写本地内存]
    E --> F[异步通知旧 center\n关闭旧 stream]
    C --> G[回复 REGISTER_ACK success=true]
    D --> G
    E --> G
```

---

## 七、请求路由

### 单机路由

```go
func (r *Router) Send(ctx context.Context, rec *registry.AgentRecord, tool string, args map[string]any) (string, error) {
    reqID := generateRequestID(r.centerID)
    ch := make(chan *tunnel.ToolResponse, 1)
    r.pending.Store(reqID, ch)
    defer r.pending.Delete(reqID)

    ctx, cancel := context.WithTimeout(ctx, time.Duration(r.cfg.RequestTimeoutSec)*time.Second)
    defer cancel()

    if rec.OwnerCenterID == r.centerID {
        if err := rec.RouteStream.Send(buildToolReq(reqID, tool, rec.Hostname, args)); err != nil {
            return "", ErrRouteStreamBroken
        }
    } else {
        go r.forwardToCenter(ctx, rec.OwnerCenterID, reqID, rec.Hostname, tool, args, ch)
    }

    select {
    case resp := <-ch:
        if resp.IsError {
            return "", errors.New(resp.ResultJson)
        }
        return resp.ResultJson, nil
    case <-ctx.Done():
        // 超时后向 agent 发取消信号，避免 agent 继续执行无用工作
        go r.sendCancel(rec, reqID)
        return "", ErrRequestTimeout
    }
}

// sendCancel 通过 stream 向 agent（经 proxy）发送 CANCEL_REQUEST，尽力而为，失败不影响调用方
func (r *Router) sendCancel(rec *registry.AgentRecord, reqID string) {
    cancelMsg := buildCancelReq(reqID)
    if rec.OwnerCenterID == r.centerID {
        _ = rec.RouteStream.Send(cancelMsg)
    } else {
        _ = r.forwardCancelToCenter(rec.OwnerCenterID, cancelMsg)
    }
}
```

超时默认 **5s**（`request_timeout_sec: 5`）。

---

## 八、多机并发查询

### 响应封装格式

所有 agent 工具的响应都包装为 `ToolMultiResult`，不直接返回裸数组，保留扩展字段：

```go
// internal/sys-mcp-center/router/result.go

type HostResult struct {
    Host  string          `json:"host"`
    Data  json.RawMessage `json:"data,omitempty"`
    Error string          `json:"error,omitempty"`
}

type ToolMultiResult struct {
    Results []HostResult `json:"results"`
    Total   int          `json:"total"`
    Success int          `json:"success"`
    Failed  int          `json:"failed"`
}
```

### SendMulti：按 center 分组并发

`SendMulti` 先从 PostgreSQL 批量查各 host 的 `owner_center_id`，然后按 center 分组并发处理：

```mermaid
sequenceDiagram
    participant AI as AI 助手
    participant CA as center-A
    participant CB as center-B
    participant A1 as agent server-01
    participant A2 as agent server-02
    participant A3 as agent server-03

    AI->>CA: target_hosts=[server-01, server-02, server-03]
    CA->>CA: 按 owner 分组\nserver-01,02 在 center-A\nserver-03 在 center-B
    par 并发
        CA->>A1: TOOL_REQUEST
        CA->>A2: TOOL_REQUEST
        CA->>CB: InternalForwardMulti{hosts=[server-03]}
    end
    CB->>A3: TOOL_REQUEST
    A1-->>CA: TOOL_RESPONSE
    A2-->>CA: TOOL_RESPONSE
    A3-->>CB: TOOL_RESPONSE
    CB-->>CA: ForwardMultiResponse
    CA-->>AI: ToolMultiResult{results:[...], total:3, success:3, failed:0}
```

```go
func (r *Router) SendMulti(ctx context.Context, hostnames []string, tool string, args map[string]any) (*ToolMultiResult, error) {
    // 1. 批量查 PostgreSQL 得到各 host 的 owner_center_id
    records, err := r.registry.LookupBatch(hostnames)
    if err != nil {
        return nil, err
    }

    // 2. 对不存在的 host 显式返回 not-found 错误，避免静默丢失
    found := make(map[string]bool, len(records))
    for _, rec := range records {
        found[rec.Hostname] = true
    }

    // 3. 按 owner_center_id 分组
    local := []string{}
    remote := map[string][]string{}  // center_id → hostnames
    for _, rec := range records {
        if rec.OwnerCenterID == r.centerID {
            local = append(local, rec.Hostname)
        } else {
            remote[rec.OwnerCenterID] = append(remote[rec.OwnerCenterID], rec.Hostname)
        }
    }

    // 4. 并发处理，用 semaphore 限制并发度（默认 128，可配置）
    maxConcurrency := r.cfg.MaxConcurrency
    if maxConcurrency <= 0 {
        maxConcurrency = 128
    }
    sem := make(chan struct{}, maxConcurrency)
    resultCh := make(chan HostResult, len(hostnames))
    var wg sync.WaitGroup

    // 预填充不存在的 host
    for _, h := range hostnames {
        if !found[h] {
            resultCh <- HostResult{Host: h, Error: "agent not found or offline"}
        }
    }

    for _, host := range local {
        host := host
        wg.Add(1)
        go func() {
            defer wg.Done()
            sem <- struct{}{}
            defer func() { <-sem }()
            rec, _ := r.registry.Lookup(host)
            data, err := r.Send(ctx, rec, tool, args)
            if err != nil {
                resultCh <- HostResult{Host: host, Error: err.Error()}
            } else {
                resultCh <- HostResult{Host: host, Data: json.RawMessage(data)}
            }
        }()
    }

    for centerID, hosts := range remote {
        centerID, hosts := centerID, hosts
        wg.Add(1)
        go func() {
            defer wg.Done()
            sem <- struct{}{}
            defer func() { <-sem }()
            results, err := r.forwardMultiToCenter(ctx, centerID, tool, args, hosts)
            if err != nil {
                // 转发失败，整批标记为错误
                for _, h := range hosts {
                    resultCh <- HostResult{Host: h, Error: err.Error()}
                }
                return
            }
            // 成功：按实际返回结果写入，不重复写 hosts
            for _, res := range results {
                resultCh <- res
            }
        }()
    }

    wg.Wait()
    close(resultCh)

    out := &ToolMultiResult{Total: len(hostnames)}
    for res := range resultCh {
        out.Results = append(out.Results, res)
        if res.Error != "" {
            out.Failed++
        } else {
            out.Success++
        }
    }
    return out, nil
}
```

### 跨实例转发 proto 补充

```protobuf
service CenterInternalService {
    // 单台转发
    rpc ForwardTool(ForwardToolRequest) returns (ForwardToolResponse);
    // 批量转发（SendMulti 使用）
    rpc ForwardToolMulti(ForwardToolMultiRequest) returns (ForwardToolMultiResponse);
}

message ForwardToolMultiRequest {
    string   request_id       = 1;
    repeated string hostnames = 2;
    string   tool_name        = 3;
    string   params_json      = 4;
}

message ForwardToolMultiResponse {
    repeated ForwardToolResponse results = 1;
}

message ForwardToolResponse {
    string result_json    = 1;
    bool   is_error       = 2;
    string agent_hostname = 3;  // 批量转发时映射结果回具体 host，单台转发时同样填充
}
```

---

## 九、鉴权设计

### HTTP 侧（MCP over HTTP/SSE）

```go
func BearerTokenMiddleware(tokens []string, next http.Handler) http.Handler {
    set := make(map[string]struct{}, len(tokens))
    for _, t := range tokens { set[t] = struct{}{} }
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        auth := r.Header.Get("Authorization")
        token, ok := strings.CutPrefix(auth, "Bearer ")
        if !ok || token == "" {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        if _, valid := set[token]; !valid {
            http.Error(w, "forbidden", http.StatusForbidden)
            return
        }
        next.ServeHTTP(w, r)
    })
}
```

### gRPC 侧（agent/proxy 注册）

1. mTLS：`tls.RequireAndVerifyClientCert`
2. 注册 token：`REGISTER_REQ.token` 在 `TunnelServiceImpl.Connect` 中验证，失败则关闭 stream

### 内部 gRPC（center 实例间）

`internal_grpc_address`（默认 9444）同样启用 mTLS，复用同一 CA，但颁发独立的 `center-internal` 证书，仅 center 实例持有。纯网络隔离（防火墙 / NetworkPolicy）不足以防止内网横向渗透后直接调用 `CenterInternalService.ForwardTool`。

**已知限制**：Bearer Token 热重载仅更新内存中的 token 集合，已认证的 HTTP SSE 长连接不会被主动踢断；若因 token 泄露需要立即失效，需重启 center 实例。

---

## 十、启动与关闭流程

`cmd/sys-mcp-center/main.go` 只负责加载配置、调用 `center.Run()`，所有启动逻辑在 `internal/sys-mcp-center/` 中：

```mermaid
flowchart TD
    Start([Center.Run]) --> InitLog[初始化日志]
    InitLog --> InitTLS[加载 TLS 证书]
    InitTLS --> InitPG[连接 PostgreSQL\nschema migration]
    InitPG --> InitRegistry[初始化 Registry\n启动 center 续约 goroutine\n启动心跳超时检测 goroutine]
    InitRegistry --> InitRouter[初始化 Router]
    InitRouter --> InitMCP[初始化 MCP Server\n注册所有工具]
    InitMCP --> StartInternal[启动内部 gRPC\ninternal_grpc_address]
    StartInternal --> StartGRPC[启动对外 gRPC\ngrpc_address]
    StartGRPC --> StartHTTP[启动 HTTP Server\nhttp_address]
    StartHTTP --> WaitSignal{等待信号}
    WaitSignal -->|SIGTERM/SIGINT| GracefulStop
    WaitSignal -->|SIGHUP| ReloadTokens[热重载 token 列表]
    ReloadTokens --> WaitSignal

    GracefulStop --> StopHTTP[HTTP Shutdown]
    StopHTTP --> DrainRouter[等待 pending 完成]
    DrainRouter --> StopGRPC[gRPC GracefulStop]
    StopGRPC --> DeregCenter[PostgreSQL 删除 center 实例注册]
    DeregCenter --> Exit([退出 0])
```

---

## 十一、测试策略

| 测试类型 | 覆盖范围                                                           | 工具                                      |
| -------- | ------------------------------------------------------------------ | ----------------------------------------- |
| 单元测试 | Registry CRUD、Router Send/Deliver、鉴权中间件、ToolMultiResult 拼装 | `testing` + `testify`                    |
| 集成测试 | 完整的 agent 注册 → MCP 调用 → 路由 → 响应链路                     | 内存 gRPC + httptest + PostgreSQL 测试实例 |
| HA 测试  | 重复注册（LWW）、跨实例路由、SendMulti 跨 center 分组              | 多 center 实例集成测试                    |
| 并发测试 | 并发 MCP 调用、Registry 并发读写                                   | `go test -race`                           |

关键测试用例：
- `TestRegistry_DuplicateRegister_SameInstance`
- `TestRegistry_DuplicateRegister_CrossInstance`
- `TestRouter_Timeout`：5s 无响应返回 `ErrRequestTimeout`
- `TestRouter_LateDelivery`：超时后响应安全丢弃
- `TestSendMulti_CrossCenter`：hosts 分布在两个 center，各自并发处理后聚合
- `TestSendMulti_PartialFailure`：3 台中 1 台超时，ToolMultiResult.Failed=1
- `TestBearerTokenMiddleware`
