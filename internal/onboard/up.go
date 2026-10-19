package onboard

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"
)

const (
	defaultPort   = "47878"
	defaultListen = "127.0.0.1:" + defaultPort
)

// Up bootstraps the hub machine: config, tokens, hub.toml, daemon.toml and
// the user services. It is idempotent: existing files and tokens are kept.
func Up(ctx context.Context, e *Env, args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	listen := fs.String("listen", "", "hub listen address (default "+defaultListen+"; use <tailscale-ip>:47878 for other machines)")
	name := fs.String("name", "", "name of this machine (default: short hostname)")
	noService := fs.Bool("no-service", false, "write config only; do not install or start services")
	withDaemon := fs.Bool("daemon", false, "also run the background daemon that works on tasks while no Claude session is open (needs gh and gitleaks)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: agent-mesh up [--listen addr] [--name host] [--daemon] [--no-service]")
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
	} else if _, err := os.Stat(e.Path("hub.toml")); errors.Is(err, os.ErrNotExist) {
		// First run: with Tailscale connected, listen on the tailnet address
		// so other machines can join without a second `up --listen`.
		if ip := tailscaleIP(ctx, e); ip != "" {
			*listen = net.JoinHostPort(ip, defaultPort)
			e.printf("Tailscale connected: the hub listens on %s (tailnet only)\n", *listen)
		}
	}
	eff, changed, err := writeHubSide(e, host, *listen)
	if err != nil {
		return err
	}
	hubURL := "http://" + eff
	if _, err := writeClientSide(e, hubURL, *listen != ""); err != nil {
		return err
	}
	e.printf("config: %s (0700)\n", e.ConfigDir)
	if changed {
		e.printf("hub URL is now %s (client.toml and daemon.toml updated).\n", hubURL)
	}
	if !*noService {
		if err := startServices(ctx, e, changed, *withDaemon); err != nil {
			return err
		}
		if err := waitHealthy(ctx, hubURL, 15*time.Second); err != nil {
			e.printf("warning: hub not answering yet: %v (check: agent-mesh doctor)\n", err)
		} else {
			e.printf("hub is up at %s\n", hubURL)
		}
	} else {
		e.printf("services not installed (--no-service). Run: agent-mesh hub\n")
	}
	tailscaleHint(ctx, e, eff)
	e.printf("\nAdd another machine: agent-mesh invite <name>\n")
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
// macTailscaleCLI is the CLI inside the Mac app (App Store and standalone
// builds), which does not put `tailscale` on PATH. A variable for tests.
var macTailscaleCLI = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

// tailscaleBin is the tailscale CLI to run, or "" when Tailscale is absent.
func tailscaleBin(e *Env) string {
	if p, err := e.Runner.LookPath("tailscale"); err == nil {
		return p
	}
	if e.GOOS == "darwin" {
		if _, err := os.Stat(macTailscaleCLI); err == nil {
			return macTailscaleCLI
		}
	}
	return ""
}

func tailscaleIP(ctx context.Context, e *Env) string {
	bin := tailscaleBin(e)
	if bin == "" {
		return ""
	}
	out, err := e.Runner.Output(ctx, bin, "ip", "-4")
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if ip, err := netip.ParseAddr(strings.TrimSpace(first)); err == nil && ip.Is4() {
		return ip.String()
	}
	return ""
}
