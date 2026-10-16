package onboard

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/felixn678/agent-mesh/internal/config"
	"github.com/felixn678/agent-mesh/internal/hub"
)

// Invite mints host and session tokens for a new machine, registers their
// hashes in hub.toml, reloads the hub and prints the one-line join command.
func Invite(ctx context.Context, e *Env, args []string) error {
	fs := flag.NewFlagSet("invite", flag.ContinueOnError)
	urlFlag := fs.String("url", "", "hub URL the new machine should use (default: http://<hub.toml listen>)")
	const usage = "usage: agent-mesh invite <host-name> [--url http://host:port]"
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New(usage)
	}
	host := fs.Arg(0)
	if err := fs.Parse(fs.Args()[1:]); err != nil || fs.NArg() > 0 { // flags may follow the name
		return errors.New(usage)
	}
	if err := checkHostName(host); err != nil {
		return err
	}
	path := e.Path("hub.toml")
	cfg, err := hub.LoadConfig(path)
	if err != nil {
		return fmt.Errorf("cannot read hub.toml (run \"agent-mesh up\" on this machine first): %w", err)
	}
	if _, ok := cfg.HostTokens[host]; ok {
		return fmt.Errorf("host %q already has a token in hub.toml; pick another name or remove its entries to re-invite", host)
	}
	if _, ok := cfg.SessionTokens[host]; ok {
		return fmt.Errorf("host %q already has a session token in hub.toml", host)
	}
	hubURL := *urlFlag
	if hubURL == "" {
		hubURL = "http://" + cfg.Listen
	}
	if hubURL, err = ParseHubURL(hubURL); err != nil {
		return err
	}
	hostTok, err := config.NewToken()
	if err != nil {
		return err
	}
	sessTok, err := config.NewToken()
	if err != nil {
		return err
	}
	err = editHubTOML(path, func(src string) (string, error) {
		src = addToTable(src, "host_tokens", fmt.Sprintf("%q = %q", host, config.HashToken(hostTok)))
		return addToTable(src, "session_tokens", fmt.Sprintf("%q = %q", host, config.HashToken(sessTok))), nil
	})
	if err != nil {
		return err
	}
	reloadHub(ctx, e)
	localURL := "http://" + cfg.Listen
	if err := verifyEventually(ctx, localURL, sessTok, 5*time.Second); err != nil {
		e.printf("warning: new token not active yet (%v)\n  restart the hub to apply it: systemctl --user restart agent-mesh-hub (or relaunch agent-mesh hub)\n", err)
	}
	enc, err := Bundle{V: bundleVersion, URL: hubURL, Host: host, HostToken: hostTok, SessionToken: sessTok}.Encode()
	if err != nil {
		return err
	}
	if isLoopback(hubURL) {
		e.printf("warning: %s is loopback; other machines cannot reach it. Rebind with: agent-mesh up --listen <tailscale-ip>:8787\n  then re-run invite with --url http://<tailscale-ip>:8787\n", hubURL)
	}
	e.printf("\nRun this on %q (it contains secrets: send it over a private channel, do not paste it anywhere public):\n\n  agent-mesh join %s %s\n\n", host, hubURL, enc)
	return nil
}

// reloadHub asks the running hub to re-read hub.toml: through the service
// manager when the hub service is active, else through the hub's pidfile.
func reloadHub(ctx context.Context, e *Env) {
	if up, _ := e.Mgr.Running(ctx, "hub"); up {
		if err := e.Mgr.Reload(ctx, "hub"); err == nil {
			return
		} else {
			e.printf("note: service reload failed (%v); trying pidfile\n", err)
		}
	}
	if err := hub.SignalReload(e.Path(hub.PidFileName)); err != nil {
		e.printf("note: could not signal the hub (%v)\n", err)
	}
}

func isLoopback(hubURL string) bool {
	h := strings.TrimPrefix(strings.TrimPrefix(hubURL, "http://"), "https://")
	host, _, err := net.SplitHostPort(h)
	if err != nil {
		host = h
	}
	return host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1"
}
