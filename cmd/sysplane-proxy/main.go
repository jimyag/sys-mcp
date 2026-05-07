// sysplane-proxy aggregates multiple agents within an IDC and relays their
// tool requests/responses to sysplane-center.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/jimmicro/version"

	proxy "github.com/jimyag/sysplane/internal/sysplane-proxy"
)

var defaultConfigPaths = []string{
	"./sysplane-proxy.yaml",
	"/etc/sysplane/proxy.yaml",
}

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "", "path to proxy config file")
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

	if err := proxy.Run(ctx, configPath); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
