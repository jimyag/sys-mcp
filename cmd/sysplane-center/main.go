package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/jimmicro/version"

	center "github.com/jimyag/sysplane/internal/sysplane-center"
)

var defaultConfigPaths = []string{
	"./sysplane-center.yaml",
	"/etc/sysplane/center.yaml",
}

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "", "path to center config file")
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

	if err := center.Run(ctx, configPath); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
