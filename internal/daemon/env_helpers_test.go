package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/hubtest"
)

// testEnv is a real hub, a temp bare "origin" repo, fake claude/gh on a
// private bin dir and a daemon for host-a.
type testEnv struct {
	t       *testing.T
	hub     *hubtest.Hub
	origin  string
	fakeDir string
	cfg     Config
	realGL  bool // real gitleaks in use
}

func gitRun(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	fakeDir, binDir, root := t.TempDir(), t.TempDir(), t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"claude", "gh"} {
		if err := os.Symlink(self, filepath.Join(binDir, n)); err != nil {
			t.Fatal(err)
		}
	}
	gl, lookErr := exec.LookPath("gitleaks")
	if lookErr != nil {
		gl = filepath.Join(binDir, "gitleaks")
		if err := os.Symlink(self, gl); err != nil {
			t.Fatal(err)
		}
	}
	gitCfg := filepath.Join(root, "gitconfig")
	if err := os.WriteFile(gitCfg, []byte("[init]\n\tdefaultBranch = main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"FAKE_DIR": fakeDir, "GIT_CONFIG_GLOBAL": gitCfg, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.com",
		"GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.com",
		"GH_TOKEN": "gh-token-must-not-reach-claude"} {
		t.Setenv(k, v)
	}
	origin := filepath.Join(root, "origin.git")
	gitRun(t, root, "init", "-q", "--bare", "-b", "main", origin)
	seed := filepath.Join(root, "seed")
	gitRun(t, root, "clone", "-q", origin, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "add", "README.md")
	gitRun(t, seed, "commit", "-q", "-m", "init")
	gitRun(t, seed, "push", "-q", "origin", "HEAD:refs/heads/main")

	cfg := DefaultConfig()
	cfg.StateDir = filepath.Join(root, "state")
	cfg.PollInterval, cfg.HeartbeatInterval = 50*time.Millisecond, 100*time.Millisecond
	cfg.TaskTimeout, cfg.GitCallTimeout = 30*time.Second, 30*time.Second
	cfg.ClaudeBin, cfg.GHBin, cfg.GitleaksBin = filepath.Join(binDir, "claude"), filepath.Join(binDir, "gh"), gl
	cfg.GHEnvFile, cfg.ClaudeEnvFile = "", ""
	cfg.Repos = map[string]string{"demo": origin}
	cfg.ToolProfiles = map[string]ToolProfile{"default": {Allow: []string{"Read", "Edit", "Write",
		"Bash(git add *)", "Bash(git commit *)"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return &testEnv{t: t, hub: hubtest.Start(t), origin: origin, fakeDir: fakeDir, cfg: cfg, realGL: lookErr == nil}
}

// daemon builds a host-a daemon with fast retries and a quiet load sampler.
func (e *testEnv) daemon(hub hubAPI) *Daemon {
	e.t.Helper()
	if hub == nil {
		hub = e.hub.Host("host-a")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	d, err := New(e.cfg, hub, log)
	if err != nil {
		e.t.Fatal(err)
	}
	d.killGrace, d.retryBase = 2*time.Second, 20*time.Millisecond
	d.sample = func(dir string) (loadSample, error) {
		n, err := countEntries(dir)
		return loadSample{CPUPct: 1, MemAvailPct: 90, Worktrees: n}, err
	}
	return d
}

func (e *testEnv) addTask(prompt string, mut ...func(*api.TaskSpec)) api.Task {
	e.t.Helper()
	spec := api.TaskSpec{Repo: "demo", Prompt: prompt, MaxBudgetUSD: 1}
	for _, m := range mut {
		m(&spec)
	}
	task, err := e.hub.Admin().AddTask(context.Background(), spec)
	if err != nil {
		e.t.Fatal(err)
	}
	return task
}

func (e *testEnv) task(id int64) api.Task {
	e.t.Helper()
	task, err := e.hub.Admin().GetTask(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return task
}

// runOne ticks once (claiming at most one task) and waits for it to finish.
func runOne(t *testing.T, d *Daemon) {
	t.Helper()
	d.tick(context.Background())
	waitTasks(t, d)
}

func waitTasks(t *testing.T, d *Daemon) {
	t.Helper()
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("tasks did not finish")
	}
}

func (e *testEnv) ghState() ghState {
	var s ghState
	withGHState(e.fakeDir, func(st *ghState) { s = *st })
	return s
}

func (e *testEnv) remoteBranches() string {
	return gitRun(e.t, e.origin, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
}

func (e *testEnv) hostAClaim() api.ClaimedTask {
	e.t.Helper()
	return e.claimAs("host-a")
}

func (e *testEnv) claimAs(host string) api.ClaimedTask {
	e.t.Helper()
	ct, err := e.hub.Host(host).Claim(context.Background(), e.cfg.caps())
	if err != nil {
		e.t.Fatalf("claim as %s: %v", host, err)
	}
	return ct
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}
