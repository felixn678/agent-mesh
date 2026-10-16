// Package cli implements the human-facing subcommands: task, hosts, inbox, hook, setup-claude.
package cli

import (
	"context"
	"errors"
)

// Run dispatches args, which include the subcommand name ("task ...",
// "hosts ...", "inbox ...", "hook <event>", "setup-claude ..."). Skeleton: replaced by the CLI owner.
func Run(ctx context.Context, args []string) error {
	return errors.New("cli: not implemented")
}
