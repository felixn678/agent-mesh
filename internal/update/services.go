package update

import (
	"bytes"
	"context"
	"os"

	"github.com/felixn678/agent-mesh/internal/onboard"
)

// Services finds and restarts the agent-mesh services on this machine.
type Services interface {
	// Installed lists the installed services in restart order (hub first),
	// split into those that run exe and those that run another binary.
	Installed(exe string) (running, other []string)
	Restart(ctx context.Context, name string) error
}

// serviceNames is the restart order: the daemon reconnects to a fresh hub.
var serviceNames = []string{"hub", "daemon"}

// onboardServices uses the systemd --user / launchd units `up` and `join` install.
type onboardServices struct{ e *onboard.Env }

// NewServices adapts the onboarding service manager.
func NewServices(e *onboard.Env) Services { return onboardServices{e} }

func (s onboardServices) Installed(exe string) (running, other []string) {
	for _, name := range serviceNames {
		b, err := os.ReadFile(onboard.UnitPath(s.e, name))
		switch {
		case err != nil:
		case bytes.Contains(b, []byte(exe)):
			running = append(running, name)
		default:
			other = append(other, name)
		}
	}
	return running, other
}

func (s onboardServices) Restart(ctx context.Context, name string) error {
	return s.e.Mgr.Restart(ctx, name)
}
