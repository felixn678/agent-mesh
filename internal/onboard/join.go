package onboard

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// readInviteCode reads one line (the invite code) from stdin, so the secret
// stays out of argv and shell history.
func (e *Env) readInviteCode() (string, error) {
	if e.In == nil {
		return "", errors.New("no stdin to read the invite code from")
	}
	e.printf("Paste the invite code and press Enter: ")
	// On a real terminal, read without echo so the secret is not left on screen
	// or in terminal scrollback.
	if f, ok := e.In.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		e.printf("\n")
		if err != nil || strings.TrimSpace(string(b)) == "" {
			return "", errors.New("no invite code received on stdin")
		}
		return strings.TrimSpace(string(b)), nil
	}
	line, err := bufio.NewReaderSize(io.LimitReader(e.In, 64<<10), 64<<10).ReadString('\n')
	if err != nil && (err != io.EOF || strings.TrimSpace(line) == "") {
		return "", errors.New("no invite code received on stdin")
	}
	return strings.TrimSpace(line), nil
}

// Join configures this machine from an invite code: tokens, daemon.toml and
// client.toml, plus the daemon service when asked for (or already installed).
// The code carries the hub URL; an explicit URL first overrides it. Nothing is
// written if the hub cannot be reached or rejects the tokens.
func Join(ctx context.Context, e *Env, args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	noService := fs.Bool("no-service", false, "write config only; do not install or start the daemon service")
	withDaemon := fs.Bool("daemon", false, "also run the background daemon that works on tasks while no Claude session is open (needs gh and gitleaks)")
	const usage = "usage: agent-mesh join [--daemon] [--no-service]   (then paste the invite code when asked)\n       agent-mesh join [<hub-url>] <invite-code>|- [--daemon] [--no-service]"
	if err := fs.Parse(args); err != nil {
		return err
	}
	pos := fs.Args()
	urlArg := ""
	if len(pos) > 0 && looksLikeURL(pos[0]) {
		urlArg, pos = pos[0], pos[1:]
	}
	code, haveCode := "", false
	if len(pos) > 0 && (pos[0] == "-" || !strings.HasPrefix(pos[0], "-")) {
		code, haveCode, pos = pos[0], true, pos[1:] // argv form: visible in the process list and shell history
	}
	if err := fs.Parse(pos); err != nil || fs.NArg() > 0 { // flags may follow the positionals
		return errors.New(usage)
	}
	if !haveCode || code == "-" {
		var err error
		if code, err = e.readInviteCode(); err != nil {
			return err
		}
	}
	b, err := DecodeBundle(code)
	if err != nil {
		return err
	}
	if urlArg == "" {
		urlArg = b.URL
	}
	hubURL, err := ParseHubURL(urlArg)
	if err != nil {
		return err
	}
	for kind, tok := range map[string]string{"session": b.SessionToken, "host": b.HostToken} {
		if err := verifyKind(ctx, hubURL, tok, kind); err != nil {
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
	retargeted, err := writeClientSide(e, hubURL, true)
	if err != nil {
		return err
	}
	e.printf("joined hub %s as %q (config: %s)\n", hubURL, b.Host, e.ConfigDir)
	switch {
	case *noService:
		e.printf("services not installed (--no-service).\n")
	case *withDaemon || daemonInstalled(e):
		if err := installDaemon(ctx, e, retargeted); err != nil {
			return err
		}
	default:
		e.printf("daemon not installed: open Claude sessions here take this machine's tasks.\n" +
			"  To also work on tasks while no session is open: agent-mesh daemon install\n")
	}
	return nil
}

// looksLikeURL tells a hub URL argument apart from an invite code.
func looksLikeURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}
