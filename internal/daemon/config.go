package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/config"
)

// ToolProfile is a host-owned tool allow list. Tasks only name a profile.
type ToolProfile struct {
	Allow []string `toml:"allow"`
	// AllowSSHAgent passes SSH_AUTH_SOCK to workers of this profile (e.g. for
	// test suites that fetch private modules over ssh). Off by default: the
	// agent can sign as you for every key it holds.
	AllowSSHAgent bool `toml:"allow_ssh_agent"`
}

// Config is daemon.toml. Paths may start with "~/"; Load expands them.
type Config struct {
	HubURL                 string                 `toml:"hub_url"`
	HostTokenFile          string                 `toml:"host_token_file"`
	StateDir               string                 `toml:"state_dir"`
	PollInterval           time.Duration          `toml:"poll_interval"`
	HeartbeatInterval      time.Duration          `toml:"heartbeat_interval"`
	TaskTimeout            time.Duration          `toml:"task_timeout"`
	GitCallTimeout         time.Duration          `toml:"git_call_timeout"`
	MaxSlots               int                    `toml:"max_slots"` // 0 = derive from hardware
	MaxCPUPct              float64                `toml:"max_cpu_pct"`
	MinMemAvailPct         float64                `toml:"min_mem_avail_pct"`
	MaxAgentWorktrees      int                    `toml:"max_agent_worktrees"`
	StuckRetention         time.Duration          `toml:"stuck_retention"`
	LogRetention           time.Duration          `toml:"log_retention"`
	AllowedPermissionModes []string               `toml:"allowed_permission_modes"`
	MaxBudgetUSDCap        float64                `toml:"max_budget_usd_cap"`
	WorkerSettingSources   string                 `toml:"worker_setting_sources"`
	ClaudeBin              string                 `toml:"claude_bin"`
	ClaudeEnvFile          string                 `toml:"claude_env_file"`
	GHBin                  string                 `toml:"gh_bin"`
	GHEnvFile              string                 `toml:"gh_env_file"`
	GitleaksBin            string                 `toml:"gitleaks_bin"`
	Repos                  map[string]string      `toml:"repos"` // task.repo -> remote URL
	ToolProfiles           map[string]ToolProfile `toml:"tool_profiles"`

	// ConfigDir is the directory holding daemon.toml (set by LoadConfig);
	// workers may not read or edit it.
	ConfigDir string `toml:"-"`
}

// DefaultConfig returns the loopback v1 defaults (no repos, no profiles).
func DefaultConfig() Config {
	return Config{
		HubURL:                 "http://127.0.0.1:47878",
		HostTokenFile:          "~/.config/agent-mesh/host.token",
		StateDir:               "~/.local/share/agent-mesh",
		PollInterval:           30 * time.Second,
		HeartbeatInterval:      60 * time.Second,
		TaskTimeout:            3 * time.Hour,
		GitCallTimeout:         120 * time.Second,
		MaxCPUPct:              80,
		MinMemAvailPct:         25,
		MaxAgentWorktrees:      6,
		StuckRetention:         72 * time.Hour,
		LogRetention:           168 * time.Hour,
		AllowedPermissionModes: []string{"acceptEdits", "dontAsk"},
		MaxBudgetUSDCap:        5,
		WorkerSettingSources:   "project",
		ClaudeBin:              "claude",
		GHBin:                  "gh",
		GitleaksBin:            "gitleaks",
	}
}

// LoadConfig reads daemon.toml over the defaults, expands paths and
// validates. host.token and gh.env default to the config file's directory
// (what `agent-mesh up|join` writes). Without hub_url the hub URL comes from
// $AGENTMESH_URL, then client.toml next to daemon.toml, then loopback.
func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	dir := filepath.Dir(path)
	c.HubURL = ""
	c.HostTokenFile = filepath.Join(dir, "host.token")
	c.GHEnvFile = filepath.Join(dir, "gh.env")
	if err := config.LoadTOML(path, &c); err != nil {
		return c, err
	}
	if c.HubURL == "" {
		c.HubURL = fallbackHubURL(dir)
	}
	if err := c.expandPaths(); err != nil {
		return c, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return c, err
	}
	c.ConfigDir = abs
	return c, c.Validate()
}

func fallbackHubURL(dir string) string {
	if u := os.Getenv("AGENTMESH_URL"); u != "" {
		return u
	}
	var cl struct {
		HubURL string `toml:"hub_url"`
	}
	if config.LoadTOML(filepath.Join(dir, "client.toml"), &cl) == nil && cl.HubURL != "" {
		return cl.HubURL
	}
	return DefaultConfig().HubURL
}

func (c *Config) expandPaths() error {
	for _, p := range []*string{&c.HostTokenFile, &c.StateDir, &c.ClaudeEnvFile, &c.GHEnvFile} {
		if *p == "" {
			continue
		}
		v, err := config.ExpandHome(*p)
		if err != nil {
			return err
		}
		if *p, err = filepath.Abs(v); err != nil {
			return err
		}
	}
	return nil
}

// canonicalRepos rekeys [repos] the way the hub stores task repos, so a
// URL-style key matches tasks whatever spelling the config used.
func canonicalRepos(in map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(in))
	for k, v := range in {
		c, err := api.CanonicalRepo(k)
		if err != nil {
			return nil, fmt.Errorf("repos: %q: %w", k, err)
		}
		if _, dup := out[c]; dup {
			return nil, fmt.Errorf("repos: %q duplicates another entry", k)
		}
		out[c] = v
	}
	return out, nil
}

// caps lists what this host can run; the hub filters claims by it.
func (c Config) caps() api.ClaimRequest {
	r := api.ClaimRequest{Modes: slices.Clone(c.AllowedPermissionModes), BudgetCap: c.MaxBudgetUSDCap}
	for k := range c.Repos {
		r.Repos = append(r.Repos, k)
	}
	for k := range c.ToolProfiles {
		r.Profiles = append(r.Profiles, k)
	}
	slices.Sort(r.Repos)
	slices.Sort(r.Profiles)
	return r
}

// fits re-checks a claimed task against local caps (defense in depth).
func (c Config) fits(t api.ClaimedTask) error {
	switch {
	case c.Repos[t.Repo] == "":
		return fmt.Errorf("repo %q not configured", t.Repo)
	case !slices.Contains(c.AllowedPermissionModes, t.PermissionMode) || !api.ValidPermissionMode(t.PermissionMode):
		return fmt.Errorf("permission mode %q not allowed", t.PermissionMode)
	case len(c.ToolProfiles[t.ToolProfile].Allow) == 0:
		return fmt.Errorf("tool profile %q not configured", t.ToolProfile)
	case !(t.MaxBudgetUSD > 0 && t.MaxBudgetUSD <= c.MaxBudgetUSDCap):
		return fmt.Errorf("budget %v over cap", t.MaxBudgetUSD)
	case !api.ValidBaseBranch(t.BaseBranch):
		return fmt.Errorf("base branch %q refused", t.BaseBranch)
	case t.ID < 1 || t.Fence < 1:
		return fmt.Errorf("bad id/fence")
	}
	return nil
}
