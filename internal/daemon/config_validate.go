package daemon

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

// bareShell are allow entries that would hand the worker an unrestricted shell.
var bareShell = []string{"Bash", "Bash(*)", "Bash(**)"}

// Validate checks the config. It refuses permission modes that skip prompts
// entirely and env files other users could read.
func (c Config) Validate() error {
	if u, err := url.Parse(c.HubURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("hub_url must be an http(s) URL")
	}
	if c.StateDir == "" || !filepath.IsAbs(c.StateDir) {
		return fmt.Errorf("state_dir must be an absolute path (after ~ expansion)")
	}
	for name, d := range map[string]time.Duration{"poll_interval": c.PollInterval,
		"heartbeat_interval": c.HeartbeatInterval, "task_timeout": c.TaskTimeout,
		"git_call_timeout": c.GitCallTimeout, "stuck_retention": c.StuckRetention, "log_retention": c.LogRetention} {
		if d <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	if c.MaxSlots < 0 || c.MaxSlots > 16 {
		return fmt.Errorf("max_slots must be 0 (auto) .. 16")
	}
	if c.MaxAgentWorktrees < 1 || c.MaxCPUPct <= 0 || c.MinMemAvailPct < 0 {
		return fmt.Errorf("max_agent_worktrees, max_cpu_pct must be positive and min_mem_avail_pct >= 0")
	}
	if len(c.AllowedPermissionModes) == 0 {
		return fmt.Errorf("allowed_permission_modes must not be empty")
	}
	for _, m := range c.AllowedPermissionModes {
		if !api.ValidPermissionMode(m) {
			return fmt.Errorf("allowed_permission_modes: %q refused (only acceptEdits, dontAsk, plan; never bypassPermissions/auto)", m)
		}
	}
	if !(c.MaxBudgetUSDCap > 0 && c.MaxBudgetUSDCap <= api.MaxBudgetUSD) {
		return fmt.Errorf("max_budget_usd_cap must be > 0 and <= %v", api.MaxBudgetUSD)
	}
	if err := validSettingSources(c.WorkerSettingSources); err != nil {
		return err
	}
	if c.ClaudeBin == "" || c.GHBin == "" || c.GitleaksBin == "" {
		return fmt.Errorf("claude_bin, gh_bin and gitleaks_bin are required")
	}
	for repo, u := range c.Repos {
		if _, err := api.CanonicalRepo(repo); err != nil || u == "" || strings.HasPrefix(u, "-") {
			return fmt.Errorf("repos: invalid entry %q", repo)
		}
	}
	for name, p := range c.ToolProfiles {
		if err := validProfile(name, p); err != nil {
			return err
		}
	}
	for _, f := range []string{c.ClaudeEnvFile, c.GHEnvFile} {
		if err := checkPrivateFile(f); err != nil {
			return err
		}
	}
	return nil
}

func validSettingSources(s string) error {
	parts := strings.Split(s, ",")
	for _, p := range parts {
		if p != "user" && p != "project" && p != "local" {
			return fmt.Errorf("worker_setting_sources: %q must be a comma list of user, project, local", s)
		}
	}
	return nil
}

func validProfile(name string, p ToolProfile) error {
	if !api.ValidHostName(name) {
		return fmt.Errorf("tool_profiles: invalid profile name %q", name)
	}
	if len(p.Allow) == 0 {
		return fmt.Errorf("tool_profiles.%s: allow must not be empty", name)
	}
	for _, a := range p.Allow {
		if a == "" || strings.HasPrefix(a, "-") || strings.ContainsAny(a, "\n\r") {
			return fmt.Errorf("tool_profiles.%s: invalid entry %q", name, a)
		}
		if slices.Contains(bareShell, a) {
			return fmt.Errorf("tool_profiles.%s: %q grants an unrestricted shell; scope Bash rules", name, a)
		}
	}
	return nil
}

// checkPrivateFile accepts "" (unset), a missing file (treated as unset:
// gh may then use its own login) or a file with no group/world bits.
func checkPrivateFile(path string) error {
	if path == "" {
		return nil
	}
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("env file: %w", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("env file %s has mode %v; require 0600", path, st.Mode().Perm())
	}
	return nil
}
