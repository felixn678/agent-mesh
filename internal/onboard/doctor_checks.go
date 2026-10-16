package onboard

import (
	"context"
	"fmt"
	"github.com/BurntSushi/toml"
	"os"
	"strings"
	"time"
)

// hubURLFor resolves the hub URL like the CLI: $AGENTMESH_URL, else
// client.toml, else daemon.toml, else the loopback default.
func hubURLFor(e *Env) (url, source string) {
	if u := os.Getenv("AGENTMESH_URL"); u != "" {
		return u, "$AGENTMESH_URL"
	}
	for _, n := range []string{"client.toml", "daemon.toml"} {
		if u := readHubURL(e.Path(n)); u != "" {
			return u, n
		}
	}
	return "http://" + defaultListen, "default"
}

func hubReachable(ctx context.Context, url, src string) Result {
	name := "hub reachable"
	if err := waitHealthy(ctx, url, 0); err != nil {
		return Result{name, Fail, fmt.Sprintf("%s (from %s): %v", url, src, err),
			"start it: systemctl --user start agent-mesh-hub (hub machine), check the URL and Tailscale (other machines)"}
	}
	return Result{name, Pass, url + " (from " + src + ")", ""}
}

func authResult(ctx context.Context, tokenFile, url, tok string) Result {
	name := "auth " + strings.TrimSuffix(tokenFile, ".token")
	if err := verifyToken(ctx, url, tok); err != nil {
		return Result{name, Fail, err.Error(), "agent-mesh invite <host> on the hub, then agent-mesh join again; on the hub, SIGHUP reloads hub.toml"}
	}
	return Result{name, Pass, "accepted by hub", ""}
}

// toolChecks verifies the CLIs workers and sessions need.
func toolChecks(ctx context.Context, e *Env) []Result {
	var rs []Result
	probe := func(name, bin string, args []string, hint string) {
		if _, err := e.Runner.LookPath(bin); err != nil {
			rs = append(rs, Result{name, Fail, bin + " not in PATH", hint})
			return
		}
		if args == nil {
			rs = append(rs, Result{name, Pass, bin + " found", ""})
			return
		}
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if out, err := e.Runner.Output(cctx, bin, args...); err != nil {
			rs = append(rs, Result{name, Fail, strings.TrimSpace(firstLine(out)) + " (" + err.Error() + ")", hint})
			return
		}
		rs = append(rs, Result{name, Pass, bin + " " + strings.Join(args, " ") + " ok", ""})
	}
	probe("claude", "claude", []string{"auth", "status"}, "install Claude Code and run: claude auth login")
	probe("git", "git", nil, "install git")
	probe("gh auth", "gh", []string{"auth", "status"}, "install GitHub CLI and run: gh auth login")
	probe("gitleaks", "gitleaks", nil, "install: go install github.com/zricethezav/gitleaks/v8@latest")
	return rs
}

func firstLine(b []byte) string {
	s, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return s
}

// serviceChecks warn (not fail) when a service is down: the hub may be run by
// hand with --no-service, and reachability is checked separately.
func serviceChecks(ctx context.Context, e *Env, isHub bool) []Result {
	var rs []Result
	names := []string{"daemon"}
	if isHub {
		names = []string{"hub", "daemon"}
	}
	for _, n := range names {
		up, err := e.Mgr.Running(ctx, n)
		switch {
		case err != nil:
			rs = append(rs, Result{"service " + n, Warn, err.Error(), "run agent-mesh " + n + " manually"})
		case !up:
			rs = append(rs, Result{"service " + n, Warn, "not running", "agent-mesh up (or join) installs it; or run: agent-mesh " + n})
		default:
			rs = append(rs, Result{"service " + n, Pass, "running", ""})
		}
	}
	return rs
}

func tailscaleCheck(ctx context.Context, e *Env) Result {
	if _, err := e.Runner.LookPath("tailscale"); err != nil {
		return Result{"tailscale", Warn, "not installed (only needed for multi-machine use)", "https://tailscale.com/download"}
	}
	if ip := tailscaleIP(ctx, e); ip != "" {
		return Result{"tailscale", Pass, "tailnet address " + ip, ""}
	}
	return Result{"tailscale", Warn, "installed but not connected", "tailscale up"}
}

// readHubURL extracts hub_url from a TOML file, tolerating other keys (the
// daemon validates its own file); "" when absent or unreadable.
func readHubURL(path string) string {
	var c struct {
		HubURL string `toml:"hub_url"`
	}
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return ""
	}
	return c.HubURL
}
