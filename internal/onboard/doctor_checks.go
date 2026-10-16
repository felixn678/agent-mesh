package onboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/config"
	"github.com/felixn678/agent-mesh/internal/version"
)

// hubURLFor resolves the hub URL exactly like the other CLI commands.
func hubURLFor(e *Env) (url, source string) { return config.HubURL(e.ConfigDir) }

// sessionURLCheck warns when $AGENTMESH_URL points CLI commands at a
// different hub than the config files that hooks, the waiter and the MCP
// server use (they ignore the variable on purpose).
func sessionURLCheck(e *Env, url, src string) (Result, bool) {
	surl, ssrc := config.SessionHubURL(e.ConfigDir)
	if src != "$AGENTMESH_URL" || url == surl {
		return Result{}, false
	}
	return Result{"session hub", Warn,
		fmt.Sprintf("hooks, waiter and MCP use %s (from %s); $AGENTMESH_URL=%s only affects CLI commands", surl, ssrc, url),
		"unset AGENTMESH_URL, or set hub_url in client.toml to the hub you mean"}, true
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
	if err := verifyKind(ctx, url, tok, strings.TrimSuffix(tokenFile, ".token")); err != nil {
		return Result{name, Fail, err.Error(), "agent-mesh invite <host> on the hub, then agent-mesh join again; on the hub, SIGHUP reloads hub.toml"}
	}
	return Result{name, Pass, "accepted by hub as " + strings.TrimSuffix(tokenFile, ".token") + " token", ""}
}

// versionCheck compares this binary with the hub's version. Hubs too old to
// report one, and dev or pseudo-version builds, pass as "unknown"; ok is false
// when the hub could not be asked (auth and reachability report that).
func versionCheck(ctx context.Context, url, tok string) (Result, bool) {
	name := "version"
	who, err := client.New(url, tok).WhoAmI(ctx)
	if err != nil {
		return Result{}, false
	}
	if skew := version.LocalSkew(version.Version, who.HubVersion); skew != "" {
		return Result{name, Warn, skew, "agent-mesh update (on the machine with the older version)"}, true
	}
	if _, known := version.Compare(version.Version, who.HubVersion); !known {
		hub := who.HubVersion
		if hub == "" {
			hub = "unreported"
		}
		return Result{name, Pass, version.Version + " (hub " + hub + "; not compared)", ""}, true
	}
	return Result{name, Pass, version.Version + ", same as the hub", ""}, true
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
	if s == "{" { // JSON status output (claude auth status when logged out)
		return "not logged in"
	}
	return s
}

// serviceChecks warn (not fail) when a service is down: the hub may be run by
// hand with --no-service, and reachability is checked separately. hubReachable
// tells a hub started by hand apart from a hub that is really down.
func serviceChecks(ctx context.Context, e *Env, isHub, hubReachable bool) []Result {
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
		case !up && n == "hub" && hubReachable:
			rs = append(rs, Result{"service hub", Warn, "not installed/running as a service (hub reachable — started manually?)",
				"agent-mesh up installs the hub service so it survives logout and reboot"})
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
