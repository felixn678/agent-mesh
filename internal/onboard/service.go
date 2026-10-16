package onboard

import (
	"context"
	"fmt"
)

// Unit describes a per-user background service running `agent-mesh <Args>`.
type Unit struct {
	Name string   // "hub" or "daemon"
	Args []string // arguments after the binary
	Desc string
}

// Manager installs and controls user services. Production uses systemd
// (Linux) or launchd (macOS); tests inject a fake.
type Manager interface {
	// Install writes the unit, enables it and starts it. A running service is
	// restarted only if its definition changed.
	Install(ctx context.Context, u Unit) error
	// Restart restarts an installed service.
	Restart(ctx context.Context, name string) error
	// Reload sends SIGHUP to a running service.
	Reload(ctx context.Context, name string) error
	// Running reports whether the service is active.
	Running(ctx context.Context, name string) (bool, error)
}

// NewManager picks the service manager for e.GOOS.
func NewManager(e *Env) Manager {
	switch e.GOOS {
	case "linux":
		return &systemdManager{e: e}
	case "darwin":
		return &launchdManager{e: e}
	}
	return unsupportedManager{os: e.GOOS}
}

type unsupportedManager struct{ os string }

func (m unsupportedManager) err() error {
	return fmt.Errorf("no service manager support on %s; run the commands manually or use --no-service", m.os)
}
func (m unsupportedManager) Install(context.Context, Unit) error           { return m.err() }
func (m unsupportedManager) Restart(context.Context, string) error         { return m.err() }
func (m unsupportedManager) Reload(context.Context, string) error          { return m.err() }
func (m unsupportedManager) Running(context.Context, string) (bool, error) { return false, m.err() }
