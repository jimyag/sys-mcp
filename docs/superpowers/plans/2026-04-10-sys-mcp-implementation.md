# sys-mcp Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this plan.

**Goal:** Build sys-mcp — 4 binaries (agent/proxy/center/client) that let AI assistants query remote physical machine resources via MCP.

**Architecture:** agent connects via gRPC bidirectional stream to proxy or center; center exposes MCP over HTTP/SSE; client bridges stdio MCP to center over HTTP. Phase 1: single-instance center with in-memory registry.

**Tech Stack:** Go, `google.golang.org/grpc`, `github.com/modelcontextprotocol/go-sdk`, `github.com/shirou/gopsutil/v4`, `gopkg.in/yaml.v3`

---

## Chunk 1: Foundation

### Task 1: Dependencies + Taskfile + directory scaffold

- [ ] Add deps to go.mod: `go get google.golang.org/grpc@latest google.golang.org/protobuf@latest github.com/shirou/gopsutil/v4@latest gopkg.in/yaml.v3@latest golang.org/x/sync@latest`
- [ ] Check if `github.com/modelcontextprotocol/go-sdk` exists; if not use `github.com/mark3labs/mcp-go@latest` as fallback
- [ ] Create `Taskfile.yaml` (4 binaries: agent/proxy/center/client, proto gen, test, lint tasks)
- [ ] Create all directory structure: `api/proto/`, `api/tunnel/`, `internal/pkg/stream/`, `internal/pkg/tlsconf/`, `internal/sys-mcp-agent/config/`, `internal/sys-mcp-agent/collector/`, `internal/sys-mcp-agent/fileops/`, `internal/sys-mcp-agent/apiproxy/`, `internal/sys-mcp-center/config/`, `internal/sys-mcp-center/registry/`, `internal/sys-mcp-center/router/`, `internal/sys-mcp-center/mcp/`, `internal/sys-mcp-proxy/config/`, `internal/sys-mcp-proxy/registry/`, `internal/sys-mcp-proxy/tunnel/`, `cmd/sys-mcp-agent/`, `cmd/sys-mcp-center/`, `cmd/sys-mcp-proxy/`, `cmd/sys-mcp-client/`, `deploy/config/`, `deploy/systemd/`
- [ ] Run `go mod tidy`
- [ ] Commit: `chore: add dependencies and project scaffold`

### Task 2: Proto + code generation

- [ ] Create `api/proto/tunnel.proto` with: `TunnelService.Connect(stream TunnelMessage) returns (stream TunnelMessage)`, all message types (RegisterRequest/Ack, Heartbeat/Ack, ToolRequest/Response, ErrorResponse, CancelRequest), NodeType enum
- [ ] Install protoc plugins: `go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`
- [ ] Generate: `protoc --go_out=. --go-grpc_out=. --go_opt=Mapi/proto/tunnel.proto=github.com/jimyag/sysplane/api/tunnel --go-grpc_opt=Mapi/proto/tunnel.proto=github.com/jimyag/sysplane/api/tunnel api/proto/tunnel.proto`
- [ ] Verify: `go build ./api/...`
- [ ] Commit: `feat: add tunnel proto and generated gRPC code`

### Task 3: internal/pkg/tlsconf

- [ ] Create `internal/pkg/tlsconf/tlsconf.go`: `LoadServerTLS(cert, key, ca string) (*tls.Config, error)` — loads x509 key pair, if ca non-empty sets `ClientAuth=RequireAndVerifyClientCert`; `LoadClientTLS(cert, key, ca string) (*tls.Config, error)` — loads client cert if provided, custom CA pool if ca non-empty
- [ ] Test: missing file returns error; empty CA uses system pool; valid cert pair loads successfully
- [ ] Run: `go test ./internal/pkg/tlsconf/... -v`
- [ ] Commit: `feat(pkg): add tlsconf mTLS loader`

### Task 4: internal/pkg/stream — Dialer

- [ ] Create `internal/pkg/stream/dialer.go`: `DialerConfig{Endpoint, TLS, RegisterMsg, HeartbeatInterval, ReconnectMaxDelay, OnMessage}`, `Dialer.Run(ctx)` — dials gRPC, sends RegisterRequest, waits for RegisterAck, starts heartbeat goroutine, reads loop; reconnects with exponential backoff (200ms initial, configurable max)
- [ ] Create `internal/pkg/stream/util.go`: `newRequestID(prefix)` using atomic counter
- [ ] Create `internal/pkg/stream/dialer.go` Send method: store active stream reference with mutex, expose `Send(*tunnel.TunnelMessage) error`
- [ ] Test: mock gRPC server that accepts register → sends ack → closes stream; verify Dialer reconnects at least twice in 2s
- [ ] Run: `go test ./internal/pkg/stream/... -v -timeout 10s`
- [ ] Commit: `feat(pkg): add stream Dialer with reconnect and heartbeat`

### Task 5: internal/pkg/stream — TunnelStream

- [ ] Create `internal/pkg/stream/stream.go`: `TunnelStream` interface with `Send`, `RemoteAddr`, `Context`, `ID`, `Close`; `WrapServerStream(id string, srv TunnelService_ConnectServer, cancel func()) TunnelStream` — mutex-protected Send, peer addr from gRPC context
- [ ] Commit: `feat(pkg): add TunnelStream server-side wrapper`

---

## Chunk 2: sys-mcp-agent

### Task 6: Agent config

- [ ] Create `internal/sys-mcp-agent/config/config.go`: `AgentConfig{Upstream, Security, Logging, ToolTimeoutSec}` with YAML tags; `Load(path)` reads file, applies defaults (MaxFileSizeMB=100, ReconnectMaxDelaySec=5, ToolTimeoutSec=25, blocked dirs=[/proc,/sys,/dev]), validates (address+token required)
- [ ] Test: valid config loads; missing address/token fails; defaults applied
- [ ] Commit: `feat(agent): add config loader`

### Task 7: Agent fileops — PathGuard

- [ ] Create `internal/sys-mcp-agent/fileops/guard.go`: `NewPathGuard(allowed, blocked []string) *PathGuard`; `Check(path) error` — normalize with `filepath.Clean`, blocklist first (prefix match with separator), then allowlist
- [ ] Test: allow-all; blocklist denies; allowlist denies outside; blocklist overrides allowlist; `../../etc/passwd` traversal blocked
- [ ] Commit: `feat(agent): add PathGuard`

### Task 8: Agent fileops — list_directory, stat_file, check_path_exists

- [ ] `listdir.go`: `ListDirectory(ctx, guard, paramsJSON)` → `os.ReadDir`, skip hidden if !ShowHidden, return `{path, items:[{name,type,size,modified_at,permissions}]}`
- [ ] `stat.go`: `StatFile(ctx, guard, paramsJSON)` → `os.Lstat`, return full stat including inode (via `syscall.Stat_t`); `CheckPathExists(ctx, guard, paramsJSON)` → lightweight exists+type check
- [ ] Test each with temp dirs
- [ ] Commit: `feat(agent): add list_directory, stat_file, check_path_exists`

### Task 9: Agent fileops — read_file + search_file_content

- [ ] `readfile.go`: `ReadFile(ctx, guard, maxMB, paramsJSON)` — binary detection (NUL byte check on first 512B), size limit for full reads, head/tail via ring buffer, max_lines with truncation flag
- [ ] `search.go`: `SearchFileContent(ctx, guard, paramsJSON)` — `bufio.Scanner` line-by-line, `regexp.Compile`, support -i/-v/-n/-c/-A/-B/-C/-E/-F/-m params, skip lines > maxLineLength
- [ ] Test: binary file rejected; size limit; head/tail correctness; grep flags
- [ ] Commit: `feat(agent): add read_file and search_file_content`

### Task 10: Agent collector

- [ ] `collector/collector.go`: `Collector` interface with `GetHardwareInfo(ctx, paramsJSON) (string, error)`
- [ ] `collector/hardware.go`: `gopsutilCollector` — parallel collect via `errgroup`: cpu.Info+cpu.Percent, mem.VirtualMemory, disk.Partitions+disk.Usage, net.Interfaces+net.IOCounters, host.Info; marshal to JSON matching API doc shape
- [ ] Test: calls GetHardwareInfo, validates JSON has cpu/memory/disks/network/system keys
- [ ] Commit: `feat(agent): add hardware info collector`

### Task 11: Agent apiproxy

- [ ] `apiproxy/guard.go`: `PortGuard{allowPrivileged, allowedPorts}` — rejects privileged ports if !allow; rejects ports not in allowedPorts if list non-empty
- [ ] `apiproxy/proxy.go`: `Proxy.Call(ctx, paramsJSON)` — validate port via PortGuard, build `http://127.0.0.1:PORT/PATH`, custom `DialContext` that resolves to `127.0.0.1` (blocks DNS rebinding), execute request, return `{request_url,status_code,headers,body,body_is_json,duration_ms}`
- [ ] Test: external IP blocked by DialContext; privileged port denied; successful local call
- [ ] Commit: `feat(agent): add proxy_local_api`

### Task 12: Agent main struct + cmd

- [ ] `internal/sys-mcp-agent/agent.go`: `Agent{cfg, handlers map[string]ToolHandler, dialer}` — `New(cfg)` wires guard/portGuard/collector/aproxy, registers all 7 tool handlers; `Run(ctx)` loads TLS if configured, creates Dialer with RegisterRequest, calls `dialer.Run`; `dispatch(msg)` — in goroutine: timeout ctx, call handler, send response via `dialer.Send`
- [ ] `cmd/sys-mcp-agent/main.go`: parse `--config` flag, find default config paths, load config, `signal.NotifyContext`, `agent.New(cfg).Run(ctx)`
- [ ] Build: `go build ./cmd/sys-mcp-agent/` — must compile
- [ ] Commit: `feat(agent): add Agent struct and cmd entry point`

---

## Chunk 3: sys-mcp-center

### Task 13: Center config

- [ ] `internal/sys-mcp-center/config/config.go`: `CenterConfig{Listen{HTTPAddress,GRPCAddress,TLS}, Auth{ClientTokens,AgentTokens}, Router{RequestTimeoutSec}, Logging}` — defaults: RequestTimeoutSec=5
- [ ] Test: valid loads, defaults applied
- [ ] Commit: `feat(center): add config`

### Task 14: Center registry

- [ ] `internal/sys-mcp-center/registry/registry.go`: `AgentRecord{Hostname,IP,OS,AgentVersion,RegisteredAt,LastHeartbeat,Status,RouteStream,ProxyPath}`; `Registry` with `sync.RWMutex`; methods: `Register`, `Unregister`, `UnregisterByStream(TunnelStream) []string`, `Lookup`, `All`, `OnlineCount`, `UpdateHeartbeat`, `startOfflineChecker(ctx, timeout)` — scans every 15s, marks offline if >90s no heartbeat
- [ ] Test: concurrent Register/Lookup (-race); heartbeat timeout marks offline; UnregisterByStream returns correct hostnames
- [ ] Commit: `feat(center): add in-memory agent registry`

### Task 15: Center TunnelService + Router

- [ ] `internal/sys-mcp-center/router/router.go`: `Router{timeoutSec, pending sync.Map}`; `Send(ctx, rec, tool, args) (string, error)` — generates requestID, stores pending chan, sends TOOL_REQUEST on RouteStream, waits with ctx timeout; `Deliver(requestID, resp)` — loads chan, sends, no-op if expired
- [ ] `internal/sys-mcp-center/tunnel_svc.go` (or `internal/sys-mcp-center/mcp/tunnel.go`): implements `TunnelServiceServer.Connect` — receive first msg must be REGISTER_REQ, validate token, wrap stream, register in registry, loop handling HEARTBEAT→ack+updateHB / TOOL_RESPONSE→router.Deliver; on exit: UnregisterByStream
- [ ] Test: Router timeout returns error; late Deliver is safe (no panic/leak); TunnelService rejects bad token
- [ ] Commit: `feat(center): add Router and TunnelService handler`

### Task 16: Center MCP server

- [ ] `internal/sys-mcp-center/mcp/auth.go`: `BearerTokenMiddleware(tokens []string, next http.Handler) http.Handler`
- [ ] `internal/sys-mcp-center/mcp/tools_center.go`: `list_agents` reads registry.All(), returns `{agents:[...], total, online}`
- [ ] `internal/sys-mcp-center/mcp/tools_agent.go`: for each of 7 agent tools — `makeAgentProxyHandler(toolName, reg, router)` reads `target_host` from args (error if missing), looks up registry, calls `router.Send`, returns result; also adds `target_host` to params JSON before forwarding
- [ ] `internal/sys-mcp-center/mcp/server.go`: `NewMCPServer(reg, router) http.Handler` — create mcp.Server or equivalent, register `list_agents` + 7 agent tools, wrap with BearerTokenMiddleware
- [ ] Test: list_agents returns correct agents; missing target_host returns error; token middleware rejects bad tokens
- [ ] Commit: `feat(center): add MCP server and tools`

### Task 17: Center cmd

- [ ] `cmd/sys-mcp-center/main.go`: load config, init registry (start offline checker), init router, init TunnelService gRPC server, init MCP HTTP server, run both concurrently with `errgroup`, handle SIGTERM gracefully
- [ ] Build: `go build ./cmd/sys-mcp-center/`
- [ ] Commit: `feat(center): add cmd entry point`

---

## Chunk 4: sys-mcp-client

### Task 18: Client cmd

- [ ] `cmd/sys-mcp-client/main.go`: config struct `{center.address, center.token, center.tls, logging}`; load from `~/.config/sys-mcp-client/config.yaml` or `--config`; warn if file permissions > 0600; connect to center via MCP HTTP/SSE client; fetch tool list; create local stdio MCP server with forwarding handlers for each tool; run stdio transport; handle SIGINT
- [ ] Build: `go build ./cmd/sys-mcp-client/`
- [ ] Commit: `feat(client): add stdio→HTTP MCP bridge`

---

## Chunk 5: sys-mcp-proxy

### Task 19: Proxy config + registry

- [ ] `internal/sys-mcp-proxy/config/config.go`: `ProxyConfig{Listen{GRPCAddress,TLS}, Upstream{Address,Token,ReconnectMaxDelaySec,TLS}, Logging}`
- [ ] `internal/sys-mcp-proxy/registry/registry.go`: `DownstreamEntry{Hostname,IP,OS,ProxyPath,Stream,RegisteredAt,LastHB}`; `Registry` — `Register`, `Lookup`, `UnregisterByStream(s) []string`, `All`
- [ ] Test: UnregisterByStream clears all entries for that stream
- [ ] Commit: `feat(proxy): add config and downstream registry`

### Task 20: Proxy tunnel + cmd

- [ ] `internal/sys-mcp-proxy/tunnel/upstream.go`: wraps `stream.Dialer`; registers proxy itself (NodeType=PROXY); receives TOOL_REQUEST → look up agent in registry → store pending[requestID]=upstream_stream → forward to agent stream; receives REGISTER_ACK → route to waiting downstream
- [ ] `internal/sys-mcp-proxy/tunnel/downstream.go`: implements same `TunnelServiceServer` interface as center; on REGISTER_REQ → append own hostname to proxy_path → write local registry → forward upstream → wait ack → reply downstream; on HEARTBEAT → update registry + forward upstream; on TOOL_RESPONSE → look up pending → forward upstream
- [ ] `internal/sys-mcp-proxy/tunnel/proxy.go`: `Proxy` struct wires downstream gRPC server + upstream Dialer; on upstream reconnect: batch re-register all agents in local registry
- [ ] `cmd/sys-mcp-proxy/main.go`: load config, init registry, start upstream dialer + downstream gRPC server concurrently, handle SIGTERM
- [ ] Build: `go build ./cmd/sys-mcp-proxy/`
- [ ] Commit: `feat(proxy): add proxy tunnel and cmd`

---

## Chunk 6: Integration + Polish

### Task 21: Config examples + systemd units

- [ ] `deploy/config/sys-mcp-agent.yaml.example`: upstream.address, upstream.token, security dirs, logging
- [ ] `deploy/config/sys-mcp-proxy.yaml.example`: listen.grpc_address, upstream.address/token, logging
- [ ] `deploy/config/sys-mcp-center.yaml.example`: listen.http_address/grpc_address, auth.client_tokens/agent_tokens, logging
- [ ] `deploy/config/sys-mcp-client.yaml.example`: center.address/token, logging
- [ ] systemd unit files for agent/proxy/center (Type=simple, Restart=always, ExecStart with --config)
- [ ] Commit: `chore: add config examples and systemd units`

### Task 22: Final verification

- [ ] Run `task build` — all 4 binaries compile
- [ ] Run `go test -race ./...` — all pass
- [ ] Run `go vet ./...` — clean
- [ ] Commit: `chore: final cleanup and verification`
