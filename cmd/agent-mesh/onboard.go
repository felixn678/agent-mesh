package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/felixn678/agent-mesh/internal/cli"
	"github.com/felixn678/agent-mesh/internal/onboard"
)

// runOnboard runs up, invite, join or doctor against the real home directory,
// service manager and PATH. A successful up or join then connects Claude Code
// on this machine (setup-claude) unless --no-claude is given.
func runOnboard(ctx context.Context, args []string) error {
	env, err := onboard.NewEnv(os.Stdout)
	if err != nil {
		return err
	}
	args, connect := stripNoClaude(args)
	if err := onboard.Run(ctx, env, args); err != nil {
		return err
	}
	if connect {
		connectClaude(ctx, os.Stdout)
	}
	return nil
}

// runDaemonInstall is `agent-mesh daemon install`.
func runDaemonInstall(ctx context.Context, args []string) error {
	env, err := onboard.NewEnv(os.Stdout)
	if err != nil {
		return err
	}
	return onboard.DaemonInstall(ctx, env, args)
}

// stripNoClaude removes --no-claude from an up/join command line and reports
// whether Claude Code should be connected afterwards.
func stripNoClaude(args []string) ([]string, bool) {
	if len(args) == 0 || (args[0] != "up" && args[0] != "join") {
		return args, false
	}
	out, connect := make([]string, 0, len(args)), true
	for _, a := range args {
		if a == "--no-claude" || a == "-no-claude" {
			connect = false
			continue
		}
		out = append(out, a)
	}
	return out, connect
}

// Swapped in tests so they never touch the real ~/.claude.
var (
	claudeOnPath = func() bool { _, err := exec.LookPath("claude"); return err == nil }
	setupClaude  = func(ctx context.Context) error { return cli.Run(ctx, []string{"setup-claude"}) }
)

// connectClaude runs setup-claude, which only adds agent-mesh's own hooks and
// MCP server. A failure is reported, not returned: the machine is already
// set up and setup-claude can be re-run on its own.
func connectClaude(ctx context.Context, out io.Writer) {
	fmt.Fprintln(out, "\nConnecting Claude Code on this machine (agent-mesh setup-claude; --no-claude skips this):")
	if !claudeOnPath() {
		fmt.Fprintln(out, "  Claude Code (claude) is not on PATH. Install it, then run: agent-mesh setup-claude")
		return
	}
	if err := setupClaude(ctx); err != nil {
		fmt.Fprintf(out, "  not connected: %v\n  fix the above, then run: agent-mesh setup-claude\n", err)
		return
	}
	fmt.Fprintln(out, "\nAll set. Check this machine any time with: agent-mesh doctor")
}
