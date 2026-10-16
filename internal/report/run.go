package report

import (
	"context"
	"errors"
)

// Run is the `agent-mesh report` entry point (args exclude the subcommand).
func Run(ctx context.Context, args []string) error {
	return errors.New("report: not implemented")
}
