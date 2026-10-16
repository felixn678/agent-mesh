// Package version exposes the build version, overridden at link time:
//
//	go build -ldflags "-X github.com/felixn678/agent-mesh/internal/version.Version=1.2.3"
package version

import "runtime/debug"

// Version is the agent-mesh build version.
var Version = "dev"

// Without a link-time version, use the module version Go stamped into the
// binary: the tag for `go install .../cmd/agent-mesh@v1.2.3`, a VCS
// pseudo-version for a plain `go build` in a checkout.
func init() {
	if Version != "dev" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		Version = bi.Main.Version
	}
}
