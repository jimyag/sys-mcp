package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/jimmicro/version"

	agent "github.com/jimyag/sysplane/internal/sysplane-agent"
)

var defaultConfigPaths = []string{
	"./sysplane-agent.yaml",
	"/etc/sysplane/agent.yaml",
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := agent.Run(ctx, configPath); err != nil && err != context.Canceled {
		slog.Error("agent exited with error", "error", err)
		os.Exit(1)
	}
}
