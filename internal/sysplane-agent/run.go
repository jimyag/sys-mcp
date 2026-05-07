package agent

import (
	"context"
	"fmt"

	agentcfg "github.com/jimyag/sysplane/internal/sysplane-agent/config"
)

func Run(ctx context.Context, configPath string) error {
	cfg, err := agentcfg.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	return New(cfg).Run(ctx)
}
