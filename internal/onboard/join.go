package onboard

import (
	"context"
	"errors"
	"flag"
	"fmt"
)

// Join configures this machine from an invite bundle: tokens, daemon.toml,
// client.toml and the daemon service. Nothing is written if the hub cannot be
// reached or rejects the tokens.
func Join(ctx context.Context, e *Env, args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	noService := fs.Bool("no-service", false, "write config only; do not install or start the daemon service")
	const usage = "usage: agent-mesh join <hub-url> <invite-bundle> [--no-service]"
	if err := fs.Parse(args); err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) >= 2 { // flags may follow the positionals
		if err := fs.Parse(pos[2:]); err != nil || fs.NArg() > 0 {
			return errors.New(usage)
		}
	}
	if len(pos) < 2 {
		return errors.New(usage)
	}
	hubURL, err := ParseHubURL(pos[0])
	if err != nil {
		return err
	}
	b, err := DecodeBundle(pos[1])
	if err != nil {
		return err
	}
	for kind, tok := range map[string]string{"session": b.SessionToken, "host": b.HostToken} {
		if err := verifyToken(ctx, hubURL, tok); err != nil {
			return fmt.Errorf("%s token check failed: %w", kind, err)
		}
	}
	if err := ensureDir(e.ConfigDir); err != nil {
		return err
	}
	if err := ensureDir(e.DataDir); err != nil {
		return err
	}
	if err := installToken(e.Path("host.token"), b.HostToken); err != nil {
		return err
	}
	if err := installToken(e.Path("session.token"), b.SessionToken); err != nil {
		return err
	}
	if err := writeClientSide(e, hubURL, true); err != nil {
		return err
	}
	e.printf("joined hub %s as %q (config: %s)\n", hubURL, b.Host, e.ConfigDir)
	if *noService {
		e.printf("daemon service not installed (--no-service). Run: agent-mesh daemon\n")
	} else {
		unit := Unit{Name: "daemon", Args: []string{"daemon"}, Desc: "agent-mesh host daemon (claude -p workers)"}
		if err := e.Mgr.Install(ctx, unit); err != nil {
			return fmt.Errorf("install daemon service: %w", err)
		}
		e.printf("service agent-mesh daemon: installed and started\n")
		if e.GOOS == "linux" {
			lingerHint(ctx, e)
		}
	}
	e.printf("\nNext: agent-mesh setup-claude   (then: agent-mesh doctor)\n")
	return nil
}
