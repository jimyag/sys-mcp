// Package agent is the main package for sysplane-agent.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jimmicro/version"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"gopkg.in/yaml.v3"

	"github.com/jimyag/sysplane/api/tunnel"
	"github.com/jimyag/sysplane/internal/pkg/logutil"
	"github.com/jimyag/sysplane/internal/pkg/stream"
	"github.com/jimyag/sysplane/internal/pkg/tlsconf"
	"github.com/jimyag/sysplane/internal/sysplane-agent/apiproxy"
	"github.com/jimyag/sysplane/internal/sysplane-agent/cmdexec"
	"github.com/jimyag/sysplane/internal/sysplane-agent/collector"
	agentcfg "github.com/jimyag/sysplane/internal/sysplane-agent/config"
	"github.com/jimyag/sysplane/internal/sysplane-agent/fileops"
)

// ToolHandler is the function signature for all tool handlers.
type ToolHandler func(ctx context.Context, argsJSON string) (string, error)

// Agent is the main struct for sysplane-agent.
type Agent struct {
	cfg       *agentcfg.AgentConfig
	handlers  map[string]ToolHandler
	dialer    *stream.Dialer
	logger    *slog.Logger
	cancelFns sync.Map // requestID -> context.CancelFunc

	secMu    sync.RWMutex
	security agentcfg.Security // hot-reloadable security settings
}

// New creates an Agent, wiring all tool handlers from the config.
func New(cfg *agentcfg.AgentConfig) *Agent {
	a := &Agent{
		cfg:      cfg,
		handlers: make(map[string]ToolHandler),
		security: cfg.Security,
	}
	a.logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: logutil.ParseLevel(cfg.Logging.Level),
	}))

	// File ops: handlers read current security settings on each call so that
	// hot-reloads via set_agent_config take effect without a restart.
	a.handlers["list_directory"] = func(ctx context.Context, args string) (string, error) {
		return fileops.ListDirectory(ctx, a.pathGuard(), args)
	}
	a.handlers["stat_file"] = func(ctx context.Context, args string) (string, error) {
		return fileops.StatFile(ctx, a.pathGuard(), args)
	}
	a.handlers["check_path_exists"] = func(ctx context.Context, args string) (string, error) {
		return fileops.CheckPathExists(ctx, a.pathGuard(), args)
	}
	a.handlers["read_file"] = func(ctx context.Context, args string) (string, error) {
		return fileops.ReadFile(ctx, a.pathGuard(), a.maxFileSizeMB(), args)
	}
	a.handlers["write_file"] = func(ctx context.Context, args string) (string, error) {
		return fileops.WriteFile(ctx, a.pathGuard(), args)
	}
	a.handlers["search_file_content"] = func(ctx context.Context, args string) (string, error) {
		return fileops.SearchFileContent(ctx, a.pathGuard(), args)
	}
	a.handlers["get_hardware_info"] = func(ctx context.Context, args string) (string, error) {
		return collector.GetHardwareInfo(ctx, args)
	}
	a.handlers["run_process"] = func(ctx context.Context, args string) (string, error) {
		return cmdexec.New(a.getSecurity().AllowedCommands).Run(ctx, args)
	}
	a.handlers["proxy_local_api"] = func(ctx context.Context, args string) (string, error) {
		sec := a.getSecurity()
		return apiproxy.New(apiproxy.Config{
			AllowPrivilegedPorts: sec.AllowPrivilegedPorts,
			AllowedPorts:         sec.AllowedPorts,
		}).Call(ctx, args)
	}

	// Config management tools
	a.handlers["get_agent_config"] = a.handleGetConfig
	a.handlers["set_agent_config"] = a.handleSetConfig

	return a
}

// getSecurity returns a snapshot of the current security settings.
func (a *Agent) getSecurity() agentcfg.Security {
	a.secMu.RLock()
	defer a.secMu.RUnlock()
	return a.security
}

// setSecurity atomically replaces the in-memory security settings.
func (a *Agent) setSecurity(sec agentcfg.Security) {
	a.secMu.Lock()
	defer a.secMu.Unlock()
	a.security = sec
}

// pathGuard creates a PathGuard from the current security snapshot.
func (a *Agent) pathGuard() *fileops.PathGuard {
	sec := a.getSecurity()
	return fileops.NewPathGuard(sec.AllowedPaths, sec.BlockedPaths)
}

// maxFileSizeMB returns the current max file size limit.
func (a *Agent) maxFileSizeMB() int64 {
	return a.getSecurity().MaxFileSizeMB
}

// handleGetConfig returns the current security settings as JSON.
func (a *Agent) handleGetConfig(_ context.Context, _ string) (string, error) {
	sec := a.getSecurity()
	out, err := json.Marshal(sec)
	if err != nil {
		return "", fmt.Errorf("marshal security config: %w", err)
	}
	return string(out), nil
}

// handleSetConfig updates security settings in memory and writes them to the config file.
func (a *Agent) handleSetConfig(_ context.Context, argsJSON string) (string, error) {
	var newSec agentcfg.Security
	if err := json.Unmarshal([]byte(argsJSON), &newSec); err != nil {
		return "", fmt.Errorf("invalid security config: %w", err)
	}
	for _, cmd := range newSec.AllowedCommands {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			return "", fmt.Errorf("allowed_commands must not contain empty values")
		}
		if !filepath.IsAbs(cmd) {
			return "", fmt.Errorf("allowed_commands: %q must be an absolute path", cmd)
		}
	}
	if a.cfg.ConfigPath != "" {
		if err := a.persistSecurityConfig(newSec); err != nil {
			return "", fmt.Errorf("persist config: %w", err)
		}
	}
	a.setSecurity(newSec)
	out, _ := json.Marshal(newSec)
	return string(out), nil
}

// persistSecurityConfig reads the existing config file, replaces the security
// section, and writes it back — preserving all other fields (token, hostname, etc.).
func (a *Agent) persistSecurityConfig(sec agentcfg.Security) error {
	data, err := os.ReadFile(a.cfg.ConfigPath)
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	// Re-marshal Security through JSON→map so YAML field names match struct yaml tags.
	secJSON, _ := json.Marshal(sec)
	var secMap map[string]any
	_ = json.Unmarshal(secJSON, &secMap)
	// Convert JSON keys (snake_case) back to YAML keys (same snake_case here, safe).
	raw["security"] = yamlSecurityMap(sec)
	out, err := yaml.Marshal(raw)
	if err != nil {
		return err
	}
	return os.WriteFile(a.cfg.ConfigPath, out, 0o644)
}

// yamlSecurityMap converts Security into a map with yaml-tag-matching keys.
func yamlSecurityMap(sec agentcfg.Security) map[string]any {
	return map[string]any{
		"allowed_paths":          sec.AllowedPaths,
		"blocked_paths":          sec.BlockedPaths,
		"max_file_size_mb":       sec.MaxFileSizeMB,
		"allow_privileged_ports": sec.AllowPrivilegedPorts,
		"allowed_ports":          sec.AllowedPorts,
		"allowed_commands":       sec.AllowedCommands,
	}
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
				Hostname:     hostname,
				Os:           runtime.GOOS + "/" + runtime.GOARCH,
				NodeType:     tunnel.NodeType_NODE_TYPE_AGENT,
				Token:        a.cfg.Upstream.Token,
				AgentVersion: version.GitTag,
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

	timeout := time.Duration(a.cfg.ToolTimeoutSec) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	a.cancelFns.Store(req.RequestId, cancel)

	go func() {
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
	tlsOpt := a.cfg.Upstream.TLS
	if tlsOpt.CertFile == "" && tlsOpt.KeyFile == "" && tlsOpt.CAFile == "" {
		return insecure.NewCredentials(), nil
	}
	tlsCfg, err := tlsconf.LoadClientTLS(tlsOpt.CertFile, tlsOpt.KeyFile, tlsOpt.CAFile)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(tlsCfg), nil
}
