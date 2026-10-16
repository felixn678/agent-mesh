package onboard

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/felixn678/agent-mesh/internal/config"
)

// Status of one doctor check.
type Status int

// Check outcomes: Pass, Fail (makes doctor exit non-zero) and Warn
// (optional or best-effort things that do not fail the run).
const (
	Pass Status = iota
	Fail
	Warn
)

// Result is one line of doctor output.
type Result struct {
	Name   string
	Status Status
	Detail string
	Hint   string // how to fix; shown for Fail and Warn
}

// Doctor checks the local setup and prints a pass/fail line per check. It
// returns an error (non-zero exit) if any check failed.
func Doctor(ctx context.Context, e *Env, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: agent-mesh doctor")
	}
	results := runChecks(ctx, e)
	failed := 0
	for _, r := range results {
		mark := map[Status]string{Pass: "✓", Fail: "✗", Warn: "!"}[r.Status]
		line := mark + " " + r.Name
		if r.Detail != "" {
			line += ": " + r.Detail
		}
		e.printf("%s\n", line)
		if r.Status != Pass && r.Hint != "" {
			e.printf("    fix: %s\n", r.Hint)
		}
		if r.Status == Fail {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	e.printf("\nAll required checks passed.\n")
	return nil
}

func runChecks(ctx context.Context, e *Env) []Result {
	var rs []Result
	add := func(r ...Result) { rs = append(rs, r...) }

	st, err := os.Stat(e.ConfigDir)
	switch {
	case err != nil:
		add(Result{"config dir", Fail, e.ConfigDir + " missing", "agent-mesh up (hub machine) or agent-mesh join (other machines)"})
	case st.Mode().Perm()&0o077 != 0:
		add(Result{"config dir", Fail, fmt.Sprintf("%s has mode %v", e.ConfigDir, st.Mode().Perm()), "chmod 700 " + e.ConfigDir})
	default:
		add(Result{"config dir", Pass, e.ConfigDir, ""})
	}

	_, isHub := os.Stat(e.Path("hub.toml"))
	tokens := map[string]string{}
	names := []string{"host.token", "session.token"}
	if isHub == nil {
		names = append(names, "admin.token")
	}
	for _, n := range names {
		tok, err := config.ReadToken(e.Path(n))
		if err != nil {
			add(Result{n, Fail, err.Error(), "chmod 600 " + e.Path(n) + " (or re-run agent-mesh up/join if missing)"})
			continue
		}
		tokens[n] = tok
		add(Result{n, Pass, "present, mode 0600", ""})
	}

	url, src := hubURLFor(e)
	if r, ok := sessionURLCheck(e, url, src); ok {
		add(r)
	}
	reach := hubReachable(ctx, url, src)
	add(reach)
	for _, n := range names {
		if tok, ok := tokens[n]; ok {
			add(authResult(ctx, n, url, tok))
		}
	}
	add(toolChecks(ctx, e)...)
	add(servicePathCheck(e))
	add(serviceChecks(ctx, e, isHub == nil, reach.Status == Pass)...)
	add(tailscaleCheck(ctx, e))
	return rs
}
