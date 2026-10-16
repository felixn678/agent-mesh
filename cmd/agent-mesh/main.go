// Command agent-mesh is the single binary for the hub, host daemon and CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/felixn678/agent-mesh/internal/cli"
	"github.com/felixn678/agent-mesh/internal/daemon"
	"github.com/felixn678/agent-mesh/internal/hub"
	"github.com/felixn678/agent-mesh/internal/report"
	"github.com/felixn678/agent-mesh/internal/update"
	"github.com/felixn678/agent-mesh/internal/version"
)

const usage = `usage: agent-mesh <command> [args]

commands:
  up       set up and start the hub on this machine (config, tokens, services)
  invite   mint an invite line for another machine (hub machine)
  join     join a hub: agent-mesh join <url>, then paste the invite code
  doctor   check config, tokens, hub, auth and tools
  hub      run the hub server
  daemon   run the pull daemon on this host
  task     add/list/show/cancel/requeue tasks
  hosts    show hosts and their load (--sessions: also live Claude sessions)
  sessions list live Claude sessions on every machine
  inbox    wait for session messages (background waiter)
  hook     Claude Code hook entry point (session-start|prompt|stop|session-end|mcp-headers)
  setup-claude  opt-in install of the agentmesh MCP server and hooks for Claude Code
  report   print the morning report
  token    generate tokens (token new --admin | --host <name> | --session <name> --out <file>)
  update   update this machine to the latest release (--check, --version vX.Y.Z, --no-restart)
  version  print the version
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		var exit update.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		fmt.Fprintln(os.Stderr, "agent-mesh:", err)
		os.Exit(1)
	}
}

// run dispatches the subcommand. cli.Run receives the subcommand name too
// ("task ...", "hook ..."); the others receive only their own arguments.
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("missing command")
	}
	rest := args[1:]
	switch args[0] {
	case "hub":
		return hub.Run(ctx, rest)
	case "daemon":
		return daemon.Run(ctx, rest)
	case "task", "hosts", "sessions", "inbox", "hook", "setup-claude":
		return cli.Run(ctx, args)
	case "up", "invite", "join", "doctor":
		return runOnboard(ctx, args)
	case "report":
		return report.Run(ctx, rest)
	case "token":
		return runToken(os.Stdout, rest)
	case "update":
		return runUpdate(ctx, rest)
	case "version", "--version":
		fmt.Println(version.Version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	fmt.Fprint(os.Stderr, usage)
	return fmt.Errorf("unknown command %q", args[0])
}
