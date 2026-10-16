// Package cli implements the human-facing subcommands: task, hosts, inbox, hook, setup-claude.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
)

// cmdIO carries the process streams so tests can drive commands in-process.
type cmdIO struct {
	in       io.Reader
	out, err io.Writer
}

const usage = `usage:
  agent-mesh task add --repo <r> (--prompt <text> | --prompt-file <f|->) --budget <usd> [--base b]
                      [--mode acceptEdits|dontAsk|plan] [--profile p] [--host h] [--max-attempts n]
  agent-mesh task list [--state s] [--since 16h]
  agent-mesh task get <id>
  agent-mesh task cancel|requeue <id>        (hub machine only: needs admin.token)
  agent-mesh hosts [--verbose] [--sessions]
  agent-mesh sessions
  agent-mesh inbox --session <id> [--wait]
  agent-mesh hook session-start|prompt|stop|session-end|mcp-headers
  agent-mesh setup-claude [--uninstall] [--dry-run] [--force]`

// Run dispatches args, which include the subcommand name ("task ...",
// "hosts ...", "inbox ...", "hook <event>", "setup-claude ...").
func Run(ctx context.Context, args []string) error {
	return run(ctx, cmdIO{in: os.Stdin, out: os.Stdout, err: os.Stderr}, args)
}

func run(ctx context.Context, std cmdIO, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing command\n%s", usage)
	}
	rest := args[1:]
	switch args[0] {
	case "task":
		return runTask(ctx, std, rest)
	case "hosts":
		return runHosts(ctx, std, rest)
	case "sessions":
		return runSessions(ctx, std, rest)
	case "inbox":
		return runInbox(ctx, std, rest, defaultWaitOpts)
	case "hook":
		return runHook(ctx, std, rest)
	case "setup-claude":
		return runSetupClaude(ctx, std, rest)
	}
	return fmt.Errorf("unknown command %q\n%s", args[0], usage)
}
