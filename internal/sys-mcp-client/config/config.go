// Package config holds the configuration structure and loader for sys-mcp-client.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ClientConfig is the root configuration for sys-mcp-client.
type ClientConfig struct {
	Center  CenterConn `yaml:"center"`
	Logging Logging    `yaml:"logging"`
}

// CenterConn describes how to connect to sys-mcp-center.
type CenterConn struct {
	// URL is the base HTTP(S) URL of the center MCP endpoint, e.g. "http://localhost:18880".
	URL string `yaml:"url"`
	// Token is the bearer token used to authenticate with center.
	Token string `yaml:"token"`
}

// Logging configures the logger.
type Logging struct {
	Level string `yaml:"level"` // debug/info/warn/error. Default: info
}

// Load reads the YAML file at path and returns a validated ClientConfig.
func Load(path string) (*ClientConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("client config: read %s: %w", path, err)
	}
	var cfg ClientConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("client config: parse %s: %w", path, err)
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("client config: %w", err)
	}
	return &cfg, nil
}

func applyDefaults(cfg *ClientConfig) {
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
}

func validate(cfg *ClientConfig) error {
	if cfg.Center.URL == "" {
		return fmt.Errorf("center.url must be set")
	}
	if cfg.Center.Token == "" {
		return fmt.Errorf("center.token must be set")
	}
	return nil
}
