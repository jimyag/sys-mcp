// Package config holds the configuration structure and loader for sysplane-proxy.
package config

import (
	"fmt"
	"os"

	"github.com/jimyag/sysplane/internal/pkg/tokenauth"
	"gopkg.in/yaml.v3"
)

// ProxyConfig is the root configuration for sysplane-proxy.
type ProxyConfig struct {
	Listen   Listen   `yaml:"listen"`
	Upstream Upstream `yaml:"upstream"`
	Auth     Auth     `yaml:"auth"`
	Logging  Logging  `yaml:"logging"`
	// Hostname overrides os.Hostname() for the proxy's own identity.
	// Useful when multiple services run on the same machine.
	Hostname string `yaml:"hostname"`
}

// Listen describes the local gRPC endpoint that downstream agents (or nested
// proxies) connect to.
type Listen struct {
	// GRPCAddress is the listen address for the downstream gRPC tunnel server, e.g. ":18892".
	GRPCAddress string `yaml:"grpc_address"`
	TLS         TLS    `yaml:"tls"`
}

// TLS holds paths to TLS material.
type TLS struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	CAFile   string `yaml:"ca_file"`
}

// Upstream describes the connection to the parent (center or another proxy).
type Upstream struct {
	// Address is the host:port of the upstream gRPC tunnel service.
	Address string `yaml:"address"`
	// Token is the bearer token used to authenticate with upstream.
	Token string `yaml:"token"`
	// InsecureTLS skips certificate verification (development only).
	InsecureTLS bool `yaml:"insecure_tls"`
	TLS         TLS  `yaml:"tls"`
}

// Auth holds tokens that downstream agents/proxies must present.
type Auth struct {
	// AgentTokens are accepted bearer tokens from downstream agent connections.
	AgentTokens []string `yaml:"agent_tokens"`
	// ProxyTokens are accepted bearer tokens from downstream proxy connections.
	ProxyTokens []string `yaml:"proxy_tokens"`
}

// Logging configures the logger.
type Logging struct {
	Level  string `yaml:"level"`  // debug/info/warn/error. Default: info
	Format string `yaml:"format"` // json/text. Default: json
}

// Load reads the YAML file at path and returns a validated ProxyConfig.
func Load(path string) (*ProxyConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("proxy config: read %s: %w", path, err)
	}
	var cfg ProxyConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("proxy config: parse %s: %w", path, err)
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("proxy config: %w", err)
	}
	return &cfg, nil
}

func applyDefaults(cfg *ProxyConfig) {
	if cfg.Listen.GRPCAddress == "" {
		cfg.Listen.GRPCAddress = ":18892"
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "json"
	}
}

func validate(cfg *ProxyConfig) error {
	if cfg.Upstream.Address == "" {
		return fmt.Errorf("upstream.address must be set")
	}
	if cfg.Upstream.Token == "" {
		return fmt.Errorf("upstream.token must be set")
	}
	if len(cfg.Auth.AgentTokens) == 0 {
		return fmt.Errorf("auth.agent_tokens must have at least one token")
	}
	if len(cfg.Auth.ProxyTokens) == 0 {
		return fmt.Errorf("auth.proxy_tokens must have at least one token")
	}
	if _, err := tokenauth.NewCatalog(nil, nil, cfg.Auth.AgentTokens, cfg.Auth.ProxyTokens); err != nil {
		return fmt.Errorf("auth token validation failed: %w", err)
	}
	return nil
}
