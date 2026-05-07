package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jimyag/sysplane/internal/sysplane-agent/config"
)

func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_ValidConfig(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
upstream:
  address: "localhost:18890"
  token: "secret"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Upstream.Address != "localhost:18890" {
		t.Fatalf("unexpected address: %s", cfg.Upstream.Address)
	}
}

func TestLoad_Defaults(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
upstream:
  address: "localhost:18890"
  token: "secret"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolTimeoutSec != 25 {
		t.Fatalf("expected tool_timeout_sec=25, got %d", cfg.ToolTimeoutSec)
	}
	if cfg.ReconnectMaxDelaySec != 5 {
		t.Fatalf("expected reconnect_max_delay_sec=5, got %d", cfg.ReconnectMaxDelaySec)
	}
	if cfg.Security.MaxFileSizeMB != 100 {
		t.Fatalf("expected max_file_size_mb=100, got %d", cfg.Security.MaxFileSizeMB)
	}
	if len(cfg.Security.BlockedPaths) == 0 {
		t.Fatal("expected default blocked paths")
	}
}

func TestLoad_MissingAddress(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
upstream:
  token: "secret"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for missing address")
	}
}

func TestLoad_MissingToken(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
upstream:
  address: "localhost:18890"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := config.Load("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_InvalidAllowedCommand(t *testing.T) {
	path := writeConfig(t, t.TempDir(), `
upstream:
  address: "localhost:18890"
  token: "secret"
security:
  allowed_commands:
    - "echo"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for relative allowed command")
	}
}
