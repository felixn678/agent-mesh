package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/mcptools/format"
)

func runTask(ctx context.Context, std cmdIO, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: task add|list|get|cancel|requeue")
	}
	newClient := cliClient
	if args[0] == "cancel" || args[0] == "requeue" {
		newClient = adminClient
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	switch args[0] {
	case "add":
		return taskAdd(ctx, std, c, args[1:])
	case "list":
		return taskList(ctx, std, c, args[1:])
	case "get", "cancel", "requeue":
		return taskByID(ctx, std, c, args[0], args[1:])
	}
	return fmt.Errorf("unknown task command %q", args[0])
}

func newFlags(name string, std cmdIO) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(std.err)
	return fs
}

func taskAdd(ctx context.Context, std cmdIO, c *client.Client, args []string) error {
	fs := newFlags("task add", std)
	var spec api.TaskSpec
	prompt := fs.String("prompt", "", "task prompt")
	promptFile := fs.String("prompt-file", "", "read the prompt from a file (- = stdin)")
	fs.StringVar(&spec.Repo, "repo", "", "repo key known to hosts")
	fs.StringVar(&spec.BaseBranch, "base", "", "base branch")
	fs.Float64Var(&spec.MaxBudgetUSD, "budget", 0, "max budget in USD")
	fs.StringVar(&spec.PermissionMode, "mode", "", "acceptEdits|dontAsk|plan")
	fs.StringVar(&spec.ToolProfile, "profile", "", "host tool profile name")
	fs.StringVar(&spec.Host, "host", "", "run only on this host")
	fs.IntVar(&spec.MaxAttempts, "max-attempts", 0, "attempts before stuck")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case (*prompt == "") == (*promptFile == ""):
		return errors.New("task add: give exactly one of --prompt or --prompt-file")
	case *prompt != "":
		spec.Prompt = *prompt
	default:
		b, err := readPrompt(std.in, *promptFile)
		if err != nil {
			return err
		}
		spec.Prompt = string(b)
	}
	if err := spec.WithDefaults().Validate(); err != nil {
		return err
	}
	t, err := c.AddTask(ctx, spec)
	if err != nil {
		return err
	}
	fmt.Fprintln(std.out, "queued "+format.TaskLine(t))
	return nil
}

func readPrompt(stdin io.Reader, path string) ([]byte, error) {
	r := stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, api.MaxPromptBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read prompt: %w", err)
	}
	return b, nil
}

func taskList(ctx context.Context, std cmdIO, c *client.Client, args []string) error {
	fs := newFlags("task list", std)
	state := fs.String("state", "", "queued|running|done|stuck|cancelled")
	since := fs.Duration("since", 0, "only tasks updated within this duration")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var from time.Time
	if *since > 0 {
		from = time.Now().Add(-*since)
	}
	list, err := c.ListTasks(ctx, *state, from)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(std.out, "no tasks")
	}
	for _, t := range list {
		fmt.Fprintln(std.out, format.TaskLine(t))
	}
	return nil
}

func taskByID(ctx context.Context, std cmdIO, c *client.Client, op string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: task %s <id>", op)
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id < 1 {
		return fmt.Errorf("invalid task id %q", args[0])
	}
	var t api.Task
	switch op {
	case "get":
		if t, err = c.GetTask(ctx, id); err == nil {
			fmt.Fprint(std.out, format.TaskDetail(t))
		}
		return err
	case "cancel":
		t, err = c.CancelTask(ctx, id)
	default:
		t, err = c.RequeueTask(ctx, id)
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(std.out, map[string]string{"cancel": "cancelled", "requeue": "requeued"}[op]+" "+format.TaskLine(t))
	return nil
}

func runHosts(ctx context.Context, std cmdIO, args []string) error {
	fs := newFlags("hosts", std)
	verbose := fs.Bool("verbose", false, "include cpu model and versions")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := cliClient()
	if err != nil {
		return err
	}
	hosts, err := c.Hosts(ctx)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		fmt.Fprintln(std.out, "no hosts")
	}
	now := time.Now()
	for _, h := range hosts {
		fmt.Fprintln(std.out, format.HostLine(h, now, *verbose))
	}
	return nil
}
