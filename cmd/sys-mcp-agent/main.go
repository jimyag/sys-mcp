package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	agent "github.com/jimyag/sys-mcp/internal/sys-mcp-agent"
	agentcfg "github.com/jimyag/sys-mcp/internal/sys-mcp-agent/config"
)

var defaultConfigPaths = []string{
	"./sys-mcp-agent.yaml",
	"/etc/sys-mcp/agent.yaml",
}

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "", "path to agent config file")
	flag.Parse()

	if configPath == "" {
		for _, p := range defaultConfigPaths {
			if _, err := os.Stat(p); err == nil {
				configPath = p
				break
			}
		}
	}
	if configPath == "" {
		fmt.Fprintln(os.Stderr, "error: no config file found; use --config")
		os.Exit(1)
	}

	cfg, err := agentcfg.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load config: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	a := agent.New(cfg)
	if err := a.Run(ctx); err != nil && err != context.Canceled {
		slog.Error("agent exited with error", "error", err)
		os.Exit(1)
	}
}
