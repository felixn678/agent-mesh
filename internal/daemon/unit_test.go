package daemon

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
)

func TestDefaultSlots(t *testing.T) {
	for _, c := range []struct{ cores, ram, want int }{
		{8, 8, 1}, {8, 16, 2}, {12, 36, 3}, {16, 32, 4},
		{4, 4, 1},    // floor
		{64, 256, 4}, // cap
		{2, 64, 1}, {32, 12, 1},
	} {
		if got := DefaultSlots(c.cores, c.ram); got != c.want {
			t.Errorf("DefaultSlots(%dc, %dGB) = %d; want %d", c.cores, c.ram, got, c.want)
		}
	}
	if s, over := slotsFor(3, Hardware{Cores: 8, RAMGB: 8}); s != 3 || !over {
		t.Errorf("max_slots override = %d %v", s, over)
	}
	if s, over := slotsFor(0, Hardware{Cores: 16, RAMGB: 32}); s != 4 || over {
		t.Errorf("auto = %d %v", s, over)
	}
}

func TestThrottleReason(t *testing.T) {
	c := DefaultConfig()
	ok := loadSample{CPUPct: 10, MemAvailPct: 60, Worktrees: 1}
	for _, tc := range []struct {
		name      string
		unhealthy string
		running   int
		s         loadSample
		want      string
	}{
		{"ok", "", 0, ok, ""},
		{"unhealthy first", "auth", 9, loadSample{CPUPct: 99}, "unhealthy: auth"},
		{"slots", "", 2, ok, "slots_full"},
		{"cpu", "", 0, loadSample{CPUPct: 85.4, MemAvailPct: 60}, "cpu 85%"},
		{"mem", "", 0, loadSample{CPUPct: 10, MemAvailPct: 12.2}, "mem 12%"},
		{"worktrees", "", 0, loadSample{CPUPct: 10, MemAvailPct: 60, Worktrees: 6}, "worktrees"},
	} {
		if got := throttleReason(tc.unhealthy, tc.running, 2, tc.s, c); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func validTestConfig(t *testing.T) Config {
	c := DefaultConfig()
	c.StateDir, c.GHEnvFile = t.TempDir(), ""
	c.Repos = map[string]string{"demo": "https://github.com/example/demo.git"}
	c.ToolProfiles = map[string]ToolProfile{"default": {Allow: []string{"Read", "Bash(go test *)"}}}
	return c
}

func TestConfigValidation(t *testing.T) {
	if err := validTestConfig(t).Validate(); err != nil {
		t.Fatalf("valid config refused: %v", err)
	}
	loose := filepath.Join(t.TempDir(), "gh.env")
	if err := os.WriteFile(loose, []byte("GH_TOKEN=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Config){
		"bypassPermissions":  func(c *Config) { c.AllowedPermissionModes = []string{"bypassPermissions"} },
		"auto":               func(c *Config) { c.AllowedPermissionModes = []string{"acceptEdits", "auto"} },
		"world-readable env": func(c *Config) { c.GHEnvFile = loose },
		"bare Bash":          func(c *Config) { c.ToolProfiles["default"] = ToolProfile{Allow: []string{"Bash"}} },
		"flag-like allow":    func(c *Config) { c.ToolProfiles["default"] = ToolProfile{Allow: []string{"--x"}} },
		"bad sources":        func(c *Config) { c.WorkerSettingSources = "user,everything" },
		"relative state":     func(c *Config) { c.StateDir = "state" },
		"budget cap":         func(c *Config) { c.MaxBudgetUSDCap = 0 },
		"bad repo key":       func(c *Config) { c.Repos["../x"] = "u" },
	} {
		c := validTestConfig(t)
		mut(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoadConfigExpandsAndRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "daemon.toml")
	body := `state_dir = "~/am-state"
gh_env_file = ""
[repos]
"https://github.com/Example/Demo.git" = "https://github.com/Example/Demo.git"
[tool_profiles.default]
allow = ["Read"]
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(c.StateDir) || !strings.HasSuffix(c.StateDir, "am-state") {
		t.Fatalf("state_dir = %q", c.StateDir)
	}
	repos, err := canonicalRepos(c.Repos)
	if err != nil || repos["github.com/example/demo"] == "" {
		t.Fatalf("canonical repos = %v, %v", repos, err)
	}
	if err := os.WriteFile(p, []byte(body+"typo_key = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("unknown key accepted")
	}
}

func TestFitsRechecksClaims(t *testing.T) {
	c := validTestConfig(t)
	ok := api.ClaimedTask{ID: 1, Fence: 1, Repo: "demo", BaseBranch: "main", PermissionMode: "acceptEdits",
		ToolProfile: "default", MaxBudgetUSD: 1}
	if err := c.fits(ok); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*api.ClaimedTask){
		"repo":        func(t *api.ClaimedTask) { t.Repo = "other" },
		"mode":        func(t *api.ClaimedTask) { t.PermissionMode = "bypassPermissions" },
		"profile":     func(t *api.ClaimedTask) { t.ToolProfile = "gpu" },
		"budget":      func(t *api.ClaimedTask) { t.MaxBudgetUSD = 50 },
		"option base": func(t *api.ClaimedTask) { t.BaseBranch = "-x" },
		"agent base":  func(t *api.ClaimedTask) { t.BaseBranch = "agentmesh/task-1-f1" },
		"dotdot base": func(t *api.ClaimedTask) { t.BaseBranch = "a..b" },
		"invalid ids": func(t *api.ClaimedTask) { t.Fence = 0 },
	} {
		task := ok
		mut(&task)
		if err := c.fits(task); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestClaudeArgsIsolation(t *testing.T) {
	c := validTestConfig(t)
	args := claudeArgs(c, api.ClaimedTask{ID: 7, Fence: 1, PermissionMode: "dontAsk", ToolProfile: "default", MaxBudgetUSD: 0.5})
	joined := strings.Join(args, "\n")
	for _, want := range []string{"-p", "--strict-mcp-config", "--setting-sources\nproject", "--permission-mode\ndontAsk",
		"--max-budget-usd\n0.5", "--tools\nRead\nBash\n--allowedTools\nRead\nBash(go test *)\n--disallowedTools",
		"Bash(git push*)", "Bash(gh *)", "Bash(curl *)", "Bash(wget *)", "WebFetch",
		"Read(~/.ssh/**)", "Read(~/.config/**)", "Read(~/.claude/**)",
		"Edit(~/.gitconfig)", "Edit(~/.bashrc)", "Edit(~/.zshrc)", "Edit(~/.local/bin/**)", "Read(~/.git-credentials)",
		"Read(//" + strings.TrimPrefix(c.StateDir, "/") + "/spool/**)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q", want)
		}
	}
	if slices.Contains(args, "sh") || slices.Contains(args, "-c") || strings.Contains(joined, "worktrees/**") {
		t.Errorf("unexpected argv: %v", args)
	}
	if args[len(args)-2] != "--append-system-prompt" || !strings.Contains(args[len(args)-1], "task 7") {
		t.Errorf("append prompt: %v", args[len(args)-2:])
	}
}

func TestParseResultFixtures(t *testing.T) {
	for _, c := range []struct {
		file             string
		found, isErr     bool
		subtype, session string
		cost             float64
	}{
		{"result_success.jsonl", true, false, "success", "00000000-0000-4000-8000-000000000001", 0.00220167},
		{"result_budget.jsonl", true, true, "error_max_budget_usd", "00000000-0000-4000-8000-000000000002", 0.0123},
		{"result_truncated.jsonl", false, false, "", "00000000-0000-4000-8000-000000000003", 0},
	} {
		r, err := parseResultFile(filepath.Join("testdata", c.file))
		if err != nil {
			t.Fatal(err)
		}
		if r.Found != c.found || r.IsError != c.isErr || r.Subtype != c.subtype || r.SessionID != c.session || r.CostUSD != c.cost {
			t.Errorf("%s: %+v", c.file, r)
		}
	}
}

func TestRedact(t *testing.T) {
	in := "push failed ghp_" + strings.Repeat("a", 36) + " and github_pat_" + strings.Repeat("B", 30) +
		" key sk-ant-api03-" + strings.Repeat("x", 20) + " Authorization: Bearer abc.def.ghi123 sha " +
		strings.Repeat("f", 40) + " ok"
	out := redact(in)
	for _, leak := range []string{"ghp_", "github_pat_", "sk-ant-", "abc.def", strings.Repeat("f", 40)} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q in %q", leak, out)
		}
	}
	if !strings.HasPrefix(out, "push failed") || !strings.HasSuffix(out, " ok") {
		t.Errorf("over-redacted: %q", out)
	}
	if got := summary(strings.Repeat("é", 1500)); len(got) > maxSummaryBytes || !strings.HasPrefix(got, "é") {
		t.Errorf("summary len %d", len(got))
	}
}

func TestSensitivePaths(t *testing.T) {
	for p, want := range map[string]bool{
		".claude/settings.json": true, "sub/.claude/hooks.json": true, ".mcp.json": true, "CLAUDE.md": true,
		"docs/CLAUDE.md": true, ".github/workflows/ci.yml": true, ".gitleaks.toml": true, ".gitleaksignore": true,
		"README.md": false, "claude.go": false, ".github/CODEOWNERS": false, "x/.mcp.json.bak": false,
		// Case-insensitive filesystems load these as .claude / CLAUDE.md.
		".Claude/settings.json": true, "docs/claude.md": true, "Docs/Claude.Local.MD": true, ".MCP.json": true,
		// A symlink named .claude or .github is a file entry, not a directory prefix.
		".claude": true, "tools/.claude": true, ".github": true, ".github/workflows": true,
		".github/actions/setup/action.yml": true, "x/.github/workflows/ci.yml": false,
		"sub/.mcp.json": false, "has space/CLAUDE.md": true, "ünïcode/CLAUDE.md": true,
	} {
		if sensitive(p) != want {
			t.Errorf("sensitive(%q) = %v", p, !want)
		}
	}
}

func TestEnvFilesAndWorkerEnv(t *testing.T) {
	dir := t.TempDir()
	gh := filepath.Join(dir, "gh.env")
	if err := os.WriteFile(gh, []byte("# pat\nexport GH_TOKEN=\"tok\"\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	claudeEnv, gitEnv, err := buildEnvs([]string{"PATH=/bin", "GITHUB_TOKEN=x", "AGENTMESH_TOKEN=y", "AGENTMESH_HOOKS=on",
		"AGENTMESH_SESSION_TOKEN=s", "AGENTMESH_URL=http://hub", "SSH_AUTH_SOCK=/tmp/agent.sock", "SSH_AGENT_PID=9"}, "", gh)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(claudeEnv, []string{"PATH=/bin", "AGENTMESH_HOOKS=off"}) {
		t.Errorf("claude env = %v", claudeEnv)
	}
	if !slices.Contains(gitEnv, "GH_TOKEN=tok") || !slices.Contains(gitEnv, "GIT_TERMINAL_PROMPT=0") {
		t.Errorf("git env = %v", gitEnv)
	}
}

func TestJournalWriteIsPrivateAndAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.json")
	if err := writeJSONFile(p, journal{ID: 3, Fence: 2, Step: stepPushing}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", st.Mode(), err)
	}
	var j journal
	if err := readJSONFile(p, &j); err != nil || j.Step != stepPushing {
		t.Fatalf("read back %+v %v", j, err)
	}
}

func TestPRBodyHasNoModelText(t *testing.T) {
	j := &journal{ID: 9, Fence: 2, Base: "main", CostUSD: 1.5, SessionID: "s", Summary: "MODEL SAID THIS"}
	body := prBody(j, []string{"fix bug"})
	if strings.Contains(body, "MODEL SAID") || !strings.Contains(body, "#9") || !strings.Contains(body, "- fix bug") {
		t.Fatalf("body:\n%s", body)
	}
}

func TestLoadConfigDefaultsNextToConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTMESH_URL", "")
	p := filepath.Join(dir, "daemon.toml")
	if err := os.WriteFile(p, []byte("state_dir = \"/tmp/am\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "client.toml"), []byte("hub_url = \"http://hub.example:8787\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(p) // gh.env absent: tolerated
	if err != nil {
		t.Fatal(err)
	}
	if c.HubURL != "http://hub.example:8787" || c.HostTokenFile != filepath.Join(dir, "host.token") ||
		c.GHEnvFile != filepath.Join(dir, "gh.env") {
		t.Fatalf("config = %+v", c)
	}
	t.Setenv("AGENTMESH_URL", "http://env.example:1")
	if c, _ = LoadConfig(p); c.HubURL != "http://env.example:1" {
		t.Fatalf("env fallback = %q", c.HubURL)
	}
}

func TestSSHAgentOnlyForOptedInProfiles(t *testing.T) {
	d := &Daemon{claudeEnv: []string{"PATH=/bin", "AGENTMESH_HOOKS=off"}, sshEnv: []string{"SSH_AUTH_SOCK=/tmp/agent.sock"},
		cfg: Config{ToolProfiles: map[string]ToolProfile{"default": {Allow: []string{"Read"}},
			"with-ssh": {Allow: []string{"Read"}, AllowSSHAgent: true}}}}
	if env := d.workerEnv("default"); slices.Contains(env, "SSH_AUTH_SOCK=/tmp/agent.sock") {
		t.Errorf("default profile got the ssh agent: %v", env)
	}
	if env := d.workerEnv("with-ssh"); !slices.Contains(env, "SSH_AUTH_SOCK=/tmp/agent.sock") {
		t.Errorf("opted-in profile lacks the ssh agent: %v", env)
	}
	if len(d.claudeEnv) != 2 {
		t.Errorf("workerEnv mutated the shared env: %v", d.claudeEnv)
	}
}

// The agent-mesh config dirs and the exact secret files are off-limits to
// workers wherever they live (macOS: ~/Library/Application Support/...).
func TestDenyFloorCoversConfigDirsAndSecrets(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	c := validTestConfig(t)
	c.StateDir = "/srv/am/state"
	c.ConfigDir = "/Users/u/Library/Application Support/agent-mesh"
	c.HostTokenFile, c.GHEnvFile, c.ClaudeEnvFile = "/opt/secrets/host.token", "/opt/secrets/gh.env", "/opt/secrets/claude.env"
	floor := strings.Join(denyFloor(c), "\n")
	for _, want := range []string{
		"Read(//Users/u/Library/Application Support/agent-mesh/**)", "Edit(//Users/u/Library/Application Support/agent-mesh/**)",
		"Read(" + absRule(filepath.Join(cfgHome, "agent-mesh")) + "/**)",
		"Read(//opt/secrets/host.token)", "Edit(//opt/secrets/gh.env)", "Read(//opt/secrets/claude.env)",
		"Read(//srv/am/state/hub.db*)",
	} {
		if !strings.Contains(floor, want) {
			t.Errorf("floor missing %q", want)
		}
	}
	// A config dir that contains the worktrees would lock the worker out of
	// its own worktree; it is skipped (the exact secret files still apply).
	c.ConfigDir = "/srv/am"
	if strings.Contains(strings.Join(denyFloor(c), "\n"), "(//srv/am/**)") {
		t.Error("config dir above the worktrees was denied")
	}
}

func TestLoadConfigRecordsConfigDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	if err := os.WriteFile(path, []byte("state_dir = \""+filepath.Join(dir, "state")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil || c.ConfigDir != dir {
		t.Fatalf("ConfigDir %q (%v), want %q", c.ConfigDir, err, dir)
	}
}
