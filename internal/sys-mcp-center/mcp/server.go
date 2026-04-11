package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jimyag/sys-mcp/internal/pkg/stream"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/registry"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/router"
)

// emptySchema is used for tools that take no parameters beyond target_host.
var emptySchema = &jsonschema.Schema{Type: "object"}

// RemoteForwarder 在本地 registry 找不到 agent 时，尝试跨实例转发。
// ha.RouterBridge 实现此接口。
type RemoteForwarder interface {
	ForwardIfNeeded(ctx context.Context, requestID, targetHost, toolName, argsJSON string) (string, bool, error)
}

// CallLogger 记录工具调用日志（可选）。store.Store 实现此接口。
type CallLogger interface {
	InsertToolCallLog(ctx context.Context, requestID, centerID, targetHost, toolName, argsJSON string) error
	CompleteToolCallLog(ctx context.Context, requestID, resultJSON, errorMsg string) error
}

// NewMCPHandler builds the MCP HTTP handler for center.
// It wraps the MCP SSE endpoint with bearer token auth.
// fwd and log are optional (pass nil to disable).
func NewMCPHandler(reg *registry.Registry, rtr *router.Router, clientTokens []string, fwd RemoteForwarder, log CallLogger, instanceID string) http.Handler {
	srv := buildServer(reg, rtr, fwd, log, instanceID)
	handler := sdkmcp.NewSSEHandler(func(*http.Request) *sdkmcp.Server { return srv }, nil)
	return BearerTokenMiddleware(clientTokens, handler)
}

// BuildServerForTest exposes buildServer for use in package-external tests.
// fwd and log may be nil. This returns the raw sdkmcp.Server so tests can
// drive it via InMemoryTransport without going through the SSE HTTP layer.
func BuildServerForTest(reg *registry.Registry, rtr *router.Router, fwd RemoteForwarder, log CallLogger, instanceID string) *sdkmcp.Server {
	return buildServer(reg, rtr, fwd, log, instanceID)
}

func buildServer(reg *registry.Registry, rtr *router.Router, fwd RemoteForwarder, log CallLogger, instanceID string) *sdkmcp.Server {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "sys-mcp-center",
		Version: "0.1.0",
	}, &sdkmcp.ServerOptions{
		Instructions: "Query remote physical machine resources via registered agents.",
	})

	// list_agents: center-local tool.
	registerListAgents(srv, reg)

	// 7 agent proxy tools.
	for _, toolName := range agentToolNames {
		registerAgentProxyTool(srv, reg, rtr, toolName, fwd, log, instanceID)
	}

	// 多机并发工具
	registerMultiTool(srv, reg, rtr, "get_hardware_info_multi", fwd, log, instanceID)
	registerMultiTool(srv, reg, rtr, "list_directory_multi", fwd, log, instanceID)

	return srv
}

var agentToolNames = []string{
	"list_directory",
	"stat_file",
	"check_path_exists",
	"read_file",
	"search_file_content",
	"get_hardware_info",
	"proxy_local_api",
}

// listAgentsResult is the return shape for list_agents.
type listAgentsResult struct {
	Agents []agentInfo `json:"agents"`
	Total  int         `json:"total"`
	Online int         `json:"online"`
}

type agentInfo struct {
	Hostname     string   `json:"hostname"`
	IP           string   `json:"ip"`
	OS           string   `json:"os"`
	NodeType     string   `json:"node_type"`
	Status       string   `json:"status"`
	ProxyPath    []string `json:"proxy_path"`
	AgentVersion string   `json:"agent_version"`
}

func registerListAgents(srv *sdkmcp.Server, reg *registry.Registry) {
	tool := &sdkmcp.Tool{
		Name:        "list_agents",
		Description: "List all registered terminal agents (node_type=agent) with their online status.",
		InputSchema: emptySchema,
	}
	srv.AddTool(tool, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		records := reg.All()
		agents := make([]agentInfo, 0, len(records))
		online := 0
		for _, r := range records {
			if r.NodeType != "agent" {
				continue // proxy 节点不暴露给 AI 调用面
			}
			agents = append(agents, agentInfo{
				Hostname:     r.Hostname,
				IP:           r.IP,
				OS:           r.OS,
				NodeType:     r.NodeType,
				Status:       string(r.Status),
				ProxyPath:    r.ProxyPath,
				AgentVersion: r.AgentVersion,
			})
			if r.Status == registry.StatusOnline {
				online++
			}
		}
		res := listAgentsResult{
			Agents: agents,
			Total:  len(agents),
			Online: online,
		}
		b, _ := json.Marshal(res)
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: string(b)},
			},
		}, nil
	})
}

func registerAgentProxyTool(srv *sdkmcp.Server, reg *registry.Registry, rtr *router.Router, toolName string, fwd RemoteForwarder, log CallLogger, instanceID string) {
	tool := &sdkmcp.Tool{
		Name:        toolName,
		Description: agentToolDescription(toolName),
		InputSchema: emptySchema,
	}
	srv.AddTool(tool, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		argsRaw := req.Params.Arguments
		if argsRaw == nil {
			return errorResult("missing arguments"), nil
		}

		// Extract target_host from the args.
		var base map[string]json.RawMessage
		if err := json.Unmarshal(argsRaw, &base); err != nil {
			return errorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
		}
		targetHostRaw, ok := base["target_host"]
		if !ok {
			return errorResult("target_host is required"), nil
		}
		var targetHost string
		if err := json.Unmarshal(targetHostRaw, &targetHost); err != nil || targetHost == "" {
			return errorResult("target_host must be a non-empty string"), nil
		}

		requestID := stream.NewRequestID(toolName)

		rec := reg.Lookup(targetHost)
		if rec == nil {
			// 本实例找不到该 agent，尝试跨实例路由（HA 模式）。
			if fwd != nil {
				result, forwarded, err := fwd.ForwardIfNeeded(ctx, requestID, targetHost, toolName, string(argsRaw))
				if forwarded {
					if err != nil {
						return errorResult(err.Error()), nil
					}
					return &sdkmcp.CallToolResult{
						Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: result}},
					}, nil
				}
			}
			return errorResult(fmt.Sprintf("agent %q not found", targetHost)), nil
		}
		if rec.Status != registry.StatusOnline {
			return errorResult(fmt.Sprintf("agent %q is offline", targetHost)), nil
		}

		// 记录调用日志（可选）
		if log != nil {
			_ = log.InsertToolCallLog(ctx, requestID, instanceID, targetHost, toolName, string(argsRaw))
		}

		result, err := rtr.Send(ctx, rec, requestID, toolName, string(argsRaw))

		// 完成调用日志（可选）
		if log != nil {
			errMsg := ""
			if err != nil {
				errMsg = err.Error()
			}
			_ = log.CompleteToolCallLog(ctx, requestID, result, errMsg)
		}

		if err != nil {
			return errorResult(err.Error()), nil
		}
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: result},
			},
		}, nil
	})
}

func errorResult(msg string) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{
		IsError: true,
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: msg}},
	}
}

func agentToolDescription(name string) string {
	switch name {
	case "list_directory":
		return "List contents of a directory on a remote agent. Requires target_host."
	case "stat_file":
		return "Get metadata (size, permissions, type) of a file on a remote agent. Requires target_host."
	case "check_path_exists":
		return "Check whether a path exists on a remote agent. Requires target_host."
	case "read_file":
		return "Read the contents of a file on a remote agent. Requires target_host."
	case "search_file_content":
		return "Search file contents with pattern matching on a remote agent. Requires target_host."
	case "get_hardware_info":
		return "Get CPU, memory, disk and network info from a remote agent. Requires target_host."
	case "proxy_local_api":
		return "Call a local HTTP API on a remote agent host. Requires target_host."
	default:
		return fmt.Sprintf("Proxy tool %s. Requires target_host.", name)
	}
}

// multiToolSchema 定义多机工具的输入 schema：包含 target_hosts 数组和可选的 args。
var multiToolSchema = &jsonschema.Schema{
	Type: "object",
	Properties: map[string]*jsonschema.Schema{
		"target_hosts": {
			Type:        "array",
			Description: "目标主机名列表，留空表示查询所有在线主机",
			Items:       &jsonschema.Schema{Type: "string"},
		},
		"args": {
			Type:        "object",
			Description: "透传给底层工具的参数（与单机工具相同）",
		},
	},
}

// registerMultiTool 注册一个多机并发工具，工具名约定为 <singleToolName>_multi。
// 底层调用对应的单机工具名（去掉 _multi 后缀）。
func registerMultiTool(srv *sdkmcp.Server, reg *registry.Registry, rtr *router.Router, toolName string, fwd RemoteForwarder, log CallLogger, instanceID string) {
	singleTool := strings.TrimSuffix(toolName, "_multi")

	tool := &sdkmcp.Tool{
		Name:        toolName,
		Description: fmt.Sprintf("在多台 agent 上并发执行 %s，返回各主机的结果 map。", singleTool),
		InputSchema: multiToolSchema,
	}

	srv.AddTool(tool, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		var params struct {
			TargetHosts []string        `json:"target_hosts"`
			Args        json.RawMessage `json:"args"`
		}
		if req.Params.Arguments != nil {
			if err := json.Unmarshal(req.Params.Arguments, &params); err != nil {
				return nil, fmt.Errorf("parse args: %w", err)
			}
		}

		argsJSON := "{}"
		if params.Args != nil {
			argsJSON = string(params.Args)
		}
		requestIDBase := fmt.Sprintf("multi-%s-%d", singleTool, time.Now().UnixNano())

		// resultMap accumulates all per-host results; populated by local batch and remote forwards.
		resultMap := make(map[string]interface{})

		// Categorise explicit target hosts into: local agents, proxy errors, remote (HA) hosts.
		// When TargetHosts is empty, collect all online local agents.
		var records []*registry.AgentRecord // local agents to batch-send
		var remoteHosts []string            // hosts not found locally; try cross-instance fwd

		if len(params.TargetHosts) == 0 {
			for _, r := range reg.All() {
				if r.Status == registry.StatusOnline && r.NodeType == "agent" {
					records = append(records, r)
				}
			}
		} else {
			for _, h := range params.TargetHosts {
				r := reg.Lookup(h)
				switch {
				case r == nil:
					// Not in local registry; try HA forwarding later.
					remoteHosts = append(remoteHosts, h)
				case r.NodeType != "agent":
					// Proxy nodes cannot execute tools.
					resultMap[h] = map[string]string{"error": "target is a proxy node, not an agent"}
				default:
					records = append(records, r)
				}
			}
		}

		// --- 1. Local batch ---
		if len(records) > 0 {
			if log != nil {
				for _, rec := range records {
					rid := requestIDBase + "_" + rec.Hostname
					_ = log.InsertToolCallLog(ctx, rid, instanceID, rec.Hostname, singleTool, argsJSON)
				}
			}

			for _, mr := range rtr.SendMulti(ctx, records, requestIDBase, singleTool, argsJSON) {
				if log != nil {
					rid := requestIDBase + "_" + mr.Hostname
					if mr.Err != nil {
						_ = log.CompleteToolCallLog(ctx, rid, "", mr.Err.Error())
					} else {
						_ = log.CompleteToolCallLog(ctx, rid, mr.Result, "")
					}
				}
				if mr.Err != nil {
					resultMap[mr.Hostname] = map[string]string{"error": mr.Err.Error()}
				} else {
					var parsed interface{}
					if err := json.Unmarshal([]byte(mr.Result), &parsed); err != nil {
						resultMap[mr.Hostname] = mr.Result
					} else {
						resultMap[mr.Hostname] = parsed
					}
				}
			}
		}

		// --- 2. Cross-instance forwards (concurrent) ---
		if len(remoteHosts) > 0 {
			var wg sync.WaitGroup
			var mu sync.Mutex
			for _, h := range remoteHosts {
				wg.Add(1)
				go func(host string) {
					defer wg.Done()
					var entry interface{}
					if fwd != nil {
						reqID := requestIDBase + "_" + host
						result, forwarded, err := fwd.ForwardIfNeeded(ctx, reqID, host, singleTool, argsJSON)
						switch {
						case err != nil:
							entry = map[string]string{"error": err.Error()}
						case forwarded:
							var parsed interface{}
							if jsonErr := json.Unmarshal([]byte(result), &parsed); jsonErr != nil {
								entry = result
							} else {
								entry = parsed
							}
						default:
							entry = map[string]string{"error": "agent not found"}
						}
					} else {
						entry = map[string]string{"error": "agent not found"}
					}
					mu.Lock()
					resultMap[host] = entry
					mu.Unlock()
				}(h)
			}
			wg.Wait()
		}

		if len(resultMap) == 0 {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: `{"agents":[],"message":"没有在线的 agent"}`}},
			}, nil
		}

		b, _ := json.Marshal(resultMap)
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(b)}},
		}, nil
	})
}
