// Package config holds the configuration structure and loader for sys-mcp-center.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// CenterConfig is the root configuration for sys-mcp-center.
type CenterConfig struct {
	Listen   Listen   `yaml:"listen"`
	Auth     Auth     `yaml:"auth"`
	Router   Router   `yaml:"router"`
	Logging  Logging  `yaml:"logging"`
	Database Database `yaml:"database"`
	Metrics  Metrics  `yaml:"metrics"`
	HA       HA       `yaml:"ha"`
}

// Listen describes the network addresses for center.
type Listen struct {
	HTTPAddress string `yaml:"http_address"` // MCP/HTTP server, e.g. ":18880"
	GRPCAddress string `yaml:"grpc_address"` // tunnel gRPC server, e.g. ":18890"
	TLS         TLS    `yaml:"tls"`
}

// TLS holds paths to TLS material.
type TLS struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	CAFile   string `yaml:"ca_file"`
}

// Auth holds authentication tokens.
type Auth struct {
	// ClientTokens are accepted for MCP HTTP connections (AI clients).
	ClientTokens []string `yaml:"client_tokens"`
	// AgentTokens are accepted for gRPC tunnel connections (agents/proxies).
	AgentTokens []string `yaml:"agent_tokens"`
}

// Router holds routing-related config.
type Router struct {
	// RequestTimeoutSec is the max seconds to wait for a tool response. Default: 5.
	RequestTimeoutSec int `yaml:"request_timeout_sec"`
}

// Logging configures the logger.
type Logging struct {
	Level  string `yaml:"level"`  // debug/info/warn/error. Default: info
	Format string `yaml:"format"` // json/text. Default: json
}

// Database 配置 PostgreSQL 连接（可选）。
type Database struct {
	// Enable 为 true 时启用 PostgreSQL 持久化注册表。
	Enable bool `yaml:"enable"`
	// DSN 是 PostgreSQL 连接串，例如 "postgres://postgres:postgres@localhost:5432/sys_mcp"。
	DSN string `yaml:"dsn"`
	// MaxConns 是连接池最大连接数，默认 10。
	MaxConns int `yaml:"max_conns"`
}

// Metrics 配置 Prometheus 指标暴露（可选）。
type Metrics struct {
	// Enable 为 true 时暴露 /metrics 端点。
	Enable bool `yaml:"enable"`
	// Address 是 metrics HTTP 服务监听地址，默认 ":18891"。
	Address string `yaml:"address"`
}

// HA 配置高可用跨实例转发（可选）。
type HA struct {
	// InternalAddress 是写入 center_instances.internal_address 的可路由地址，
	// 供其他 center 实例调用本实例的 /internal/forward 使用。
	// 必须是远端可访问的 host:port，不能使用仅本地有效的 ":8080" 形式。
	InternalAddress string `yaml:"internal_address"`
	// InternalSecret 是实例间 /internal/forward 调用的共享密钥。
	// 若为空则不启用 internal endpoint 鉴权（仅适用于受信任内网）。
	InternalSecret string `yaml:"internal_secret"`
	// InternalUseTLS 为 true 时，内部转发使用 https://；否则使用 http://。
	// 应与 center 实例自身 TLS 配置保持一致。
	InternalUseTLS bool `yaml:"internal_use_tls"`
	// InternalSkipVerify 为 true 时跳过内部转发的 TLS 证书验证（适用于自签名证书）。
	InternalSkipVerify bool `yaml:"internal_skip_verify"`
}

// Load reads the YAML file at path and returns a validated CenterConfig.
func Load(path string) (*CenterConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("center config: read %s: %w", path, err)
	}
	var cfg CenterConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("center config: parse %s: %w", path, err)
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("center config: %w", err)
	}
	return &cfg, nil
}

func applyDefaults(cfg *CenterConfig) {
	if cfg.Listen.HTTPAddress == "" {
		cfg.Listen.HTTPAddress = ":18880"
	}
	if cfg.Listen.GRPCAddress == "" {
		cfg.Listen.GRPCAddress = ":18890"
	}
	if cfg.Router.RequestTimeoutSec <= 0 {
		cfg.Router.RequestTimeoutSec = 5
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "json"
	}
	if cfg.Database.MaxConns <= 0 {
		cfg.Database.MaxConns = 10
	}
	if cfg.Metrics.Address == "" {
		cfg.Metrics.Address = ":18891"
	}
}

func validate(cfg *CenterConfig) error {
	if len(cfg.Auth.AgentTokens) == 0 {
		return fmt.Errorf("auth.agent_tokens must have at least one token")
	}
	if cfg.Database.Enable && cfg.HA.InternalAddress == "" {
		return fmt.Errorf("ha.internal_address must be set when database.enable=true")
	}
	return nil
}
