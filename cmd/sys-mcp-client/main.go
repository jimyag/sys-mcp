// sys-mcp-client is a stdio MCP bridge that connects an AI assistant to
// sys-mcp-center. It discovers available tools from center and exposes them
// locally via stdio so the AI can call them transparently.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/jimmicro/version"

	client "github.com/jimyag/sys-mcp/internal/sys-mcp-client"
)

var defaultConfigPaths = []string{
	"./sys-mcp-client.yaml",
	os.Getenv("HOME") + "/.config/sys-mcp/client.yaml",
	"/etc/sys-mcp/client.yaml",
}

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "", "path to client config file")
	flag.Parse()

	if configPath == "" {
		for _, p := range defaultConfigPaths {
			if p == "" {
				continue
			}
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

	if err := client.Run(ctx, configPath); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
