package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// preflightTimeout bounds each preflight command.
const preflightTimeout = 60 * time.Second

// preflight checks every tool and credential a task needs; any failure
// keeps the host unhealthy (no claims). Secrets printed by `gh auth token`
// are discarded.
func (d *Daemon) preflight(ctx context.Context) error {
	if _, err := runCmd(ctx, preflightTimeout, "", d.gitEnv, nil, "git", "--version"); err != nil {
		return fmt.Errorf("git: %w", err)
	}
	ver, err := runCmd(ctx, preflightTimeout, "", d.claudeEnv, nil, d.cfg.ClaudeBin, "--version")
	if err != nil {
		return fmt.Errorf("claude --version: %w", err)
	}
	d.mu.Lock()
	d.claudeVersion = tail(strings.TrimSpace(ver), 60)
	d.mu.Unlock()
	out, err := runCmd(ctx, preflightTimeout, "", d.claudeEnv, nil, d.cfg.ClaudeBin, "auth", "status")
	if err != nil {
		return fmt.Errorf("claude auth status: %w", err)
	}
	var st struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if json.Unmarshal([]byte(out), &st) != nil || !st.LoggedIn {
		return fmt.Errorf("claude auth status: not logged in")
	}
	if _, err := runCmd(ctx, preflightTimeout, "", d.gitEnv, nil, d.cfg.GHBin, "auth", "token"); err != nil {
		return fmt.Errorf("gh auth token: %w", err)
	}
	for repo, remote := range d.cfg.Repos {
		if _, err := d.gitT(ctx, preflightTimeout, "", "ls-remote", "--end-of-options", remote, "HEAD"); err != nil {
			return fmt.Errorf("git ls-remote %s: %w", repo, err)
		}
	}
	if _, err := runCmd(ctx, preflightTimeout, "", d.gitEnv, nil, d.cfg.GitleaksBin, "version"); err != nil {
		return fmt.Errorf("gitleaks: %w", err)
	}
	return nil
}

// checkHealth runs preflight at start and, while the breaker is open, every
// preflightEvery; success closes the breaker.
func (d *Daemon) checkHealth(ctx context.Context, force bool) {
	d.mu.Lock()
	due := force || (d.unhealthy != "" && time.Since(d.lastPreflight) >= preflightEvery)
	if due {
		d.lastPreflight = time.Now()
	}
	d.mu.Unlock()
	if !due {
		return
	}
	err := d.preflightFn(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.unhealthy = tail(redact(err.Error()), maxUnhealthyLen)
		d.log.Error("preflight failed; not claiming", "err", d.unhealthy)
		return
	}
	if d.unhealthy != "" {
		d.log.Info("preflight passed; claiming again")
	}
	d.unhealthy, d.infraFails = "", 0
}

// preflightEvery is how often an unhealthy host re-checks itself, so a fixed
// problem (a tool installed, a login renewed) clears within minutes.
var preflightEvery = 2 * time.Minute
