package main

import (
	"context"
	"os"

	"github.com/felixn678/agent-mesh/internal/onboard"
)

// runOnboard runs up, invite, join or doctor against the real home directory,
// service manager and PATH.
func runOnboard(ctx context.Context, args []string) error {
	env, err := onboard.NewEnv(os.Stdout)
	if err != nil {
		return err
	}
	return onboard.Run(ctx, env, args)
}
