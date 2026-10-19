package onboard

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// daemonUnit runs headless `claude -p` workers for tasks queued to this
// machine while no interactive session takes them. It is optional: live
// sessions claim tasks too, and the daemon needs gh and gitleaks.
var daemonUnit = Unit{Name: "daemon", Args: []string{"daemon"}, Desc: "agent-mesh host daemon (claude -p workers)"}

// daemonInstalled reports whether this machine has the daemon service file,
// so up and join keep (and retarget) a daemon installed earlier.
func daemonInstalled(e *Env) bool {
	p := UnitPath(e, "daemon")
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// installDaemon installs and starts the daemon service, restarting it when
// its hub URL just changed (a running daemon keeps the old one until then).
func installDaemon(ctx context.Context, e *Env, retargeted bool) error {
	if err := e.Mgr.Install(ctx, daemonUnit); err != nil {
		return fmt.Errorf("install daemon service: %w", err)
	}
	if retargeted {
		if err := e.Mgr.Restart(ctx, "daemon"); err != nil {
			return fmt.Errorf("restart daemon: %w", err)
		}
	}
	e.printf("service agent-mesh daemon: installed and started\n")
	if e.GOOS == "linux" {
		lingerHint(ctx, e)
	}
	return nil
}

// DaemonInstall adds the daemon service to a machine set up by up or join
// ("agent-mesh daemon install").
func DaemonInstall(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return errors.New("usage: agent-mesh daemon install")
	}
	if _, err := os.Stat(e.Path("daemon.toml")); err != nil {
		return errors.New("this machine is not set up yet: run agent-mesh up (hub machine) or agent-mesh join first")
	}
	if err := installDaemon(ctx, e, false); err != nil {
		return err
	}
	e.printf("check its tools with: agent-mesh doctor\n")
	return nil
}
