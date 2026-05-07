// Package config holds the configuration structure and loader for sysplane-agent.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentConfig is the root configuration for sysplane-agent.
type AgentConfig struct {
	Upstream Upstream `yaml:"upstream"`
	Security Security `yaml:"security"`
	Logging  Logging  `yaml:"logging"`

	// ToolTimeoutSec is the max seconds a single tool call may run. Default: 25.
	ToolTimeoutSec int `yaml:"tool_timeout_sec"`
	// ReconnectMaxDelaySec is the max reconnect backoff in seconds. Default: 5.
	ReconnectMaxDelaySec int `yaml:"reconnect_max_delay_sec"`
	// Hostname overrides os.Hostname() for the agent's identity.
	Hostname string `yaml:"hostname"`
}

// Upstream describes how the agent connects to a center or proxy.
type Upstream struct {
	// Address is the gRPC endpoint (host:port). Required.
	Address string `yaml:"address"`
	// Token is the bearer token for authentication. Required.
	Token string `yaml:"token"`
	TLS   TLS    `yaml:"tls"`
}

// TLS holds paths to TLS material.
type TLS struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	CAFile   string `yaml:"ca_file"`
}

// Security defines file-access restrictions for tool operations.
type Security struct {
	// AllowedPaths, if non-empty, restricts file ops to these path prefixes.
	AllowedPaths []string `yaml:"allowed_paths"`
	// BlockedPaths are always denied (takes priority over AllowedPaths).
	// Default: ["/proc", "/sys", "/dev"].
	BlockedPaths []string `yaml:"blocked_paths"`
	// MaxFileSizeMB is the max file size for read_file. Default: 100.
	MaxFileSizeMB int64 `yaml:"max_file_size_mb"`
	// AllowPrivilegedPorts allows proxy_local_api to call ports <1024. Default: false.
	AllowPrivilegedPorts bool `yaml:"allow_privileged_ports"`
	// AllowedPorts, if non-empty, restricts proxy_local_api to these ports.
	AllowedPorts []int `yaml:"allowed_ports"`
	// AllowedCommands, if non-empty, whitelists absolute executable paths for run_process.
	AllowedCommands []string `yaml:"allowed_commands"`
}

// Logging configures the logger.
type Logging struct {
	Level  string `yaml:"level"`  // debug/info/warn/error. Default: info
	Format string `yaml:"format"` // json/text. Default: json
}

// Load reads the YAML file at path and returns a validated AgentConfig.
func Load(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("agent config: read %s: %w", path, err)
	}
	var cfg AgentConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("agent config: parse %s: %w", path, err)
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("agent config: %w", err)
	}
	return &cfg, nil
}

func applyDefaults(cfg *AgentConfig) {
	if cfg.ToolTimeoutSec <= 0 {
		cfg.ToolTimeoutSec = 25
	}
	if cfg.ReconnectMaxDelaySec <= 0 {
		cfg.ReconnectMaxDelaySec = 5
	}
	if cfg.Security.MaxFileSizeMB <= 0 {
		cfg.Security.MaxFileSizeMB = 100
	}
	if len(cfg.Security.BlockedPaths) == 0 {
		cfg.Security.BlockedPaths = []string{"/proc", "/sys", "/dev"}
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "json"
	}
}

func validate(cfg *AgentConfig) error {
	if cfg.Upstream.Address == "" {
		return fmt.Errorf("upstream.address is required")
	}
	if cfg.Upstream.Token == "" {
		return fmt.Errorf("upstream.token is required")
	}
	for _, command := range cfg.Security.AllowedCommands {
		command = strings.TrimSpace(command)
		if command == "" {
			return fmt.Errorf("security.allowed_commands must not contain empty values")
		}
		if !filepath.IsAbs(command) {
			return fmt.Errorf("security.allowed_commands value %q must be an absolute path", command)
		}
	}
	return nil
}
