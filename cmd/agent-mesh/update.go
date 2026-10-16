package main

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/felixn678/agent-mesh/internal/onboard"
	"github.com/felixn678/agent-mesh/internal/update"
	"github.com/felixn678/agent-mesh/internal/version"
)

// runUpdate replaces this binary with a release and restarts the services
// `up`/`join` installed on this machine.
func runUpdate(ctx context.Context, args []string) error {
	env, err := onboard.NewEnv(os.Stdout)
	if err != nil {
		return err
	}
	return update.Run(ctx, &update.Options{
		Current:  version.Version,
		Exe:      env.Binary,
		BaseURL:  os.Getenv("AGENTMESH_DOWNLOAD_BASE"),
		GOOS:     runtime.GOOS,
		GOARCH:   runtime.GOARCH,
		Home:     env.Home,
		GOPATH:   os.Getenv("GOPATH"),
		GOBIN:    os.Getenv("GOBIN"),
		HTTP:     &http.Client{Timeout: 5 * time.Minute},
		Services: update.NewServices(env),
		Out:      os.Stdout,
	}, args)
}
