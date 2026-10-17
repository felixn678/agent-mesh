package onboard

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

const defaultListen = "127.0.0.1:47878"

// Up bootstraps the hub machine: config, tokens, hub.toml, daemon.toml and
// the user services. It is idempotent: existing files and tokens are kept.
func Up(ctx context.Context, e *Env, args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	listen := fs.String("listen", "", "hub listen address (default "+defaultListen+"; use <tailscale-ip>:47878 for other machines)")
	name := fs.String("name", "", "name of this machine (default: short hostname)")
	noService := fs.Bool("no-service", false, "write config only; do not install or start services")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: agent-mesh up [--listen addr] [--name host] [--no-service]")
	}
	host := *name
	if host == "" {
		var err error
		if host, err = e.ShortHostName(); err != nil {
			return err
		}
	}
	if err := checkHostName(host); err != nil {
		return err
	}
	if *listen != "" {
		if err := checkListen(*listen); err != nil {
			return err
		}
	}
	eff, changed, err := writeHubSide(e, host, *listen)
	if err != nil {
		return err
	}
	hubURL := "http://" + eff
	if err := writeClientSide(e, hubURL, *listen != ""); err != nil {
		return err
	}
	e.printf("config: %s (0700)\n", e.ConfigDir)
	if changed {
		e.printf("hub URL is now %s (client.toml and daemon.toml updated).\n"+
			"Re-run: agent-mesh setup-claude   (Claude Code's MCP entry still points at the old URL)\n", hubURL)
	}
	if !*noService {
		if err := startServices(ctx, e, changed); err != nil {
			return err
		}
		if err := waitHealthy(ctx, hubURL, 15*time.Second); err != nil {
			e.printf("warning: hub not answering yet: %v (check: agent-mesh doctor)\n", err)
		} else {
			e.printf("hub is up at %s\n", hubURL)
		}
	} else {
		e.printf("services not installed (--no-service). Run: agent-mesh hub  and  agent-mesh daemon\n")
	}
	tailscaleHint(ctx, e, eff)
	e.printf("\nNext steps:\n  agent-mesh setup-claude          # connect Claude Code sessions on this machine\n" +
		"  agent-mesh invite <host-name>    # add another machine\n  agent-mesh doctor                # verify everything\n")
	return nil
}

// checkListen refuses wildcard and unparsable addresses: the hub is plaintext
// HTTP and must only be reachable on loopback or a Tailscale address.
func checkListen(addr string) error {
	h, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return fmt.Errorf("invalid --listen %q: expected host:port", addr)
	}
	if ip, perr := netip.ParseAddr(h); perr != nil || ip.IsUnspecified() {
		return fmt.Errorf("--listen %q: use 127.0.0.1 or your Tailscale IP, never a wildcard address", addr)
	}
	return nil
}

// tailscaleHint tells the user how to serve other machines when Tailscale is
// present and the hub is still loopback-only. It never changes the binding.
func tailscaleHint(ctx context.Context, e *Env, listen string) {
	ip := tailscaleIP(ctx, e)
	if ip == "" || !strings.HasPrefix(listen, "127.") {
		return
	}
	e.printf("\nTailscale detected (%s). To let other machines join, run:\n  agent-mesh up --listen %s:47878\n", ip, ip)
}

// tailscaleIP returns this machine's tailnet IPv4 address, or "".
func tailscaleIP(ctx context.Context, e *Env) string {
	out, err := e.Runner.Output(ctx, "tailscale", "ip", "-4")
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if ip, err := netip.ParseAddr(strings.TrimSpace(first)); err == nil && ip.Is4() {
		return ip.String()
	}
	return ""
}
