// Package version exposes the build version, overridden at link time:
//
//	go build -ldflags "-X github.com/felixn678/agent-mesh/internal/version.Version=1.2.3"
package version

// Version is the agent-mesh build version.
var Version = "dev"
