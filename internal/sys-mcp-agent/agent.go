// Package agent is the main package for sys-mcp-agent.
package agent

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/jimyag/sys-mcp/api/tunnel"
	"github.com/jimyag/sys-mcp/internal/pkg/stream"
	agentcfg "github.com/jimyag/sys-mcp/internal/sys-mcp-agent/config"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-agent/apiproxy"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-agent/collector"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-agent/fileops"
	"github.com/jimyag/sys-mcp/internal/pkg/tlsconf"
)

// ToolHandler is the function signature for all tool handlers.
type ToolHandler func(ctx context.Context, argsJSON string) (string, error)

// Agent is the main struct for sys-mcp-agent.
type Agent struct {
	cfg        *agentcfg.AgentConfig
	handlers   map[string]ToolHandler
	dialer     *stream.Dialer
	logger     *slog.Logger
	cancelFns  sync.Map // requestID -> context.CancelFunc
}

// New creates an Agent, wiring all tool handlers from the config.
func New(cfg *agentcfg.AgentConfig) *Agent {
	a := &Agent{cfg: cfg, handlers: make(map[string]ToolHandler)}
	a.logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseLogLevel(cfg.Logging.Level),
	}))

	guard := fileops.NewPathGuard(cfg.Security.AllowedPaths, cfg.Security.BlockedPaths)
	maxMB := cfg.Security.MaxFileSizeMB
	ap := apiproxy.New(apiproxy.Config{
		AllowPrivilegedPorts: cfg.Security.AllowPrivilegedPorts,
		AllowedPorts:         cfg.Security.AllowedPorts,
	})

	a.handlers["list_directory"] = func(ctx context.Context, args string) (string, error) {
		return fileops.ListDirectory(ctx, guard, args)
	}
	a.handlers["stat_file"] = func(ctx context.Context, args string) (string, error) {
		return fileops.StatFile(ctx, guard, args)
	}
	a.handlers["check_path_exists"] = func(ctx context.Context, args string) (string, error) {
		return fileops.CheckPathExists(ctx, guard, args)
	}
	a.handlers["read_file"] = func(ctx context.Context, args string) (string, error) {
		return fileops.ReadFile(ctx, guard, maxMB, args)
	}
	a.handlers["search_file_content"] = func(ctx context.Context, args string) (string, error) {
		return fileops.SearchFileContent(ctx, guard, args)
	}
	a.handlers["get_hardware_info"] = func(ctx context.Context, args string) (string, error) {
		return collector.GetHardwareInfo(ctx, args)
	}
	a.handlers["proxy_local_api"] = func(ctx context.Context, args string) (string, error) {
		return ap.Call(ctx, args)
	}

	return a
}

// Run starts the agent: connects to upstream and processes tool requests.
func (a *Agent) Run(ctx context.Context) error {
	creds, err := a.buildCredentials()
	if err != nil {
		return err
	}

	hostname := a.cfg.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	registerMsg := &tunnel.TunnelMessage{
		Payload: &tunnel.TunnelMessage_RegisterRequest{
			RegisterRequest: &tunnel.RegisterRequest{
				Hostname:  hostname,
				NodeType:  tunnel.NodeType_NODE_TYPE_AGENT,
				Token:     a.cfg.Upstream.Token,
				AgentVersion: "0.1.0",
			},
		},
	}

	a.dialer = stream.NewDialer(stream.DialerConfig{
		Endpoint:          a.cfg.Upstream.Address,
		TLSCredentials:    creds,
		RegisterMsg:       registerMsg,
		HeartbeatInterval: 30 * time.Second,
		ReconnectMaxDelay: time.Duration(a.cfg.ReconnectMaxDelaySec) * time.Second,
		OnMessage:         a.dispatch,
		OnRegisterAck: func(ack *tunnel.RegisterAck) {
			if ack.Success {
				a.logger.Info("registered with upstream", "address", a.cfg.Upstream.Address)
			} else {
				a.logger.Error("registration rejected", "reason", ack.Message)
			}
		},
	})

	a.logger.Info("starting agent", "upstream", a.cfg.Upstream.Address)
	return a.dialer.Run(ctx)
}

func (a *Agent) dispatch(msg *tunnel.TunnelMessage) {
	// 处理取消请求
	if cancel := msg.GetCancelRequest(); cancel != nil {
		if fn, ok := a.cancelFns.LoadAndDelete(cancel.RequestId); ok {
			fn.(context.CancelFunc)()
			a.logger.Debug("cancel applied", "request_id", cancel.RequestId)
		}
		return
	}

	req := msg.GetToolRequest()
	if req == nil {
		return
	}
	go func() {
		timeout := time.Duration(a.cfg.ToolTimeoutSec) * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), timeout)

		// 注册 cancel 函数供 CancelRequest 触发
		a.cancelFns.Store(req.RequestId, cancel)
		defer func() {
			cancel()
			a.cancelFns.Delete(req.RequestId)
		}()

		handler, ok := a.handlers[req.ToolName]
		if !ok {
			a.sendError(req.RequestId, "TOOL_NOT_FOUND",
				fmt.Sprintf("tool %q not found", req.ToolName))
			return
		}

		result, err := handler(ctx, req.ArgsJson)
		if err != nil {
			a.sendError(req.RequestId, "TOOL_ERROR", err.Error())
			return
		}

		_ = a.dialer.Send(&tunnel.TunnelMessage{
			Payload: &tunnel.TunnelMessage_ToolResponse{
				ToolResponse: &tunnel.ToolResponse{
					RequestId:  req.RequestId,
					ResultJson: result,
				},
			},
		})
	}()
}

func (a *Agent) sendError(requestID, code, message string) {
	_ = a.dialer.Send(&tunnel.TunnelMessage{
		Payload: &tunnel.TunnelMessage_ErrorResponse{
			ErrorResponse: &tunnel.ErrorResponse{
				RequestId: requestID,
				Code:      code,
				Message:   message,
			},
		},
	})
}

func (a *Agent) buildCredentials() (credentials.TransportCredentials, error) {
	tls := a.cfg.Upstream.TLS
	if tls.CertFile == "" && tls.KeyFile == "" && tls.CAFile == "" {
		return insecure.NewCredentials(), nil
	}
	tlsCfg, err := tlsconf.LoadClientTLS(tls.CertFile, tls.KeyFile, tls.CAFile)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(tlsCfg), nil
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ensure tls package is used (import alias avoids conflict)
var _ *tls.Config
