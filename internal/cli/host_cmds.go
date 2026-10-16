package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/mcptools/format"
)

// adminHint turns a 403 on an admin-only task command into advice: cancel and
// requeue need admin.token, which `agent-mesh up` writes on the hub machine only.
func adminHint(op string, err error) error {
	var he *client.HTTPError
	if errors.As(err, &he) && he.Status == http.StatusForbidden {
		return fmt.Errorf("task %s needs the admin token, which exists only on the hub machine: run it there "+
			"(or set AGENTMESH_TOKEN to the admin token): %w", op, err)
	}
	return err
}

// runHosts prints the daemons' load lines. Only daemons report load, so when
// none has (or always with --sessions) it also shows the machines that have
// live Claude sessions, which is what most people mean by "my machines".
func runHosts(ctx context.Context, std cmdIO, args []string) error {
	fs := newFlags("hosts", std)
	verbose := fs.Bool("verbose", false, "include cpu model and versions")
	sessions := fs.Bool("sessions", false, "also list live Claude sessions (worker:off marks opted-out ones); same as `agent-mesh sessions`")
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
	now := time.Now()
	for _, h := range hosts {
		fmt.Fprintln(std.out, format.HostLine(h, now, *verbose, c.HubVersion()))
	}
	if *sessions {
		return printSessions(ctx, std, c, now)
	}
	if len(hosts) == 0 {
		fmt.Fprintln(std.out, "no daemon has reported load yet (a machine appears here once `agent-mesh daemon` runs on it)")
		if list, err := c.ListSessions(ctx, true); err == nil && len(list) > 0 {
			fmt.Fprintln(std.out, "machines with live Claude sessions: "+sessionHosts(list)+"  (details: agent-mesh sessions)")
		}
	}
	return nil
}

// runSessions lists live Claude sessions on every machine.
func runSessions(ctx context.Context, std cmdIO, args []string) error {
	fs := newFlags("sessions", std)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: agent-mesh sessions")
	}
	c, err := cliClient()
	if err != nil {
		return err
	}
	return printSessions(ctx, std, c, time.Now())
}

func printSessions(ctx context.Context, std cmdIO, c *client.Client, now time.Time) error {
	list, err := c.ListSessions(ctx, true)
	if err != nil {
		return err
	}
	fmt.Fprintf(std.out, "sessions (%d live):\n", len(list))
	for _, s := range list {
		fmt.Fprintln(std.out, "  "+format.SessionLine(s, now, c.HubVersion()))
	}
	return nil
}

// sessionHosts summarises live sessions per machine, e.g. "laptop (1), ubuntu (3)".
func sessionHosts(list []api.Session) string {
	n := map[string]int{}
	for _, s := range list {
		n[format.Clean(s.Host)]++
	}
	names := make([]string, 0, len(n))
	for h := range n {
		names = append(names, h)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, h := range names {
		parts[i] = fmt.Sprintf("%s (%d)", h, n[h])
	}
	return strings.Join(parts, ", ")
}
