//go:build soak

package soak

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/client"
)

// fakeBin symlinks the test binary as claude, gh and gitleaks.
func fakeBin(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, n := range []string{"claude", "gh", "gitleaks"} {
		if err := os.Symlink(self, filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// makeOrigins creates one bare origin with a main branch per soak repo,
// keyed by the canonical repo key the hub stores.
func makeOrigins(t *testing.T) map[string]string {
	root := t.TempDir()
	out := map[string]string{}
	for r := 0; r <= reposPerHost; r++ { // the extra repo has no sessions: daemon-only work
		key := fmt.Sprintf("github.com/example/soak-r%d", r)
		origin := filepath.Join(root, fmt.Sprintf("soak-r%d.git", r))
		seed := filepath.Join(root, fmt.Sprintf("seed-%d", r))
		git(t, root, "init", "-q", "--bare", "-b", "main", origin)
		git(t, root, "init", "-q", "-b", "main", seed)
		if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("soak\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, seed, "add", "README.md")
		git(t, seed, "commit", "-q", "-m", "init")
		git(t, seed, "push", "-q", origin, "HEAD:refs/heads/main")
		out[key] = origin
	}
	return out
}

// daemonProc is one real `agent-mesh daemon` on a machine.
type daemonProc struct {
	m        *machine
	env      []string
	stateDir string
	log      *os.File
	cmd      *exec.Cmd
	done     chan struct{}
}

// newDaemon writes a fast daemon.toml (fake tools, local origins, no CPU
// throttling) and returns a stopped daemon.
func newDaemon(t *testing.T, m *machine, url, bin, fakeDir string, origins map[string]string, log *os.File) *daemonProc {
	state := filepath.Join(m.home, ".local", "share", "agent-mesh")
	var repos strings.Builder
	for k, v := range origins {
		fmt.Fprintf(&repos, "%q = %q\n", k, v)
	}
	toml := fmt.Sprintf(`hub_url = %q
state_dir = %q
poll_interval = "300ms"
heartbeat_interval = "2s"
task_timeout = "60s"
git_call_timeout = "20s"
max_slots = 2
max_cpu_pct = 1000
min_mem_avail_pct = 0
max_agent_worktrees = 50
claude_bin = %q
gh_bin = %q
gitleaks_bin = %q
gh_env_file = ""

[repos]
%s
[tool_profiles.default]
allow = ["Read", "Edit", "Write", "Bash(git add *)", "Bash(git commit *)"]
`, url, state, filepath.Join(bin, "claude"), filepath.Join(bin, "gh"), filepath.Join(bin, "gitleaks"), repos.String())
	if err := os.WriteFile(filepath.Join(m.cfg, "daemon.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append([]string{}, m.env...)
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + bin + ":/usr/bin:/bin"
		}
	}
	env = append(env, "FAKE_DIR="+fakeDir, "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=soak", "GIT_AUTHOR_EMAIL=soak@example.com",
		"GIT_COMMITTER_NAME=soak", "GIT_COMMITTER_EMAIL=soak@example.com")
	return &daemonProc{m: m, env: env, stateDir: state, log: log}
}

func (d *daemonProc) start() {
	cmd := exec.Command(d.m.bin, "daemon")
	cmd.Env, cmd.Stdout, cmd.Stderr = d.env, d.log, d.log
	if err := cmd.Start(); err != nil {
		d.m.t.Fatal(err)
	}
	d.cmd, d.done = cmd, make(chan struct{})
	go func() { _ = cmd.Wait(); close(d.done) }()
}

func (d *daemonProc) signal(sig syscall.Signal) {
	_ = d.cmd.Process.Signal(sig)
	select {
	case <-d.done:
	case <-time.After(70 * time.Second):
		_ = d.cmd.Process.Kill()
		d.m.t.Errorf("daemon did not exit after %v", sig)
		<-d.done
	}
}

type runRec struct {
	ID    int64 `json:"id"`
	Fence int64 `json:"fence"`
	PID   int   `json:"pid"`
}

// liveRun returns a run record whose claude is running.
func (d *daemonProc) liveRun() (runRec, bool) {
	ents, _ := os.ReadDir(filepath.Join(d.stateDir, "run"))
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var r runRec
		b, err := os.ReadFile(filepath.Join(d.stateDir, "run", e.Name()))
		if err == nil && json.Unmarshal(b, &r) == nil && r.PID > 0 && syscall.Kill(r.PID, 0) == nil {
			return r, true
		}
	}
	return runRec{}, false
}

// killResult records the daemon SIGKILL experiment.
type killResult struct {
	done                       bool
	task, fence                int64
	attemptsBefore, attemptsAt int
	fenceAfter                 int64
	stateAfter, reasonAfter    string
	orphanAliveAfterKill       bool
	orphanGoneAfterRestart     bool
}

// killMidTask SIGKILLs d while its claude runs, restarts it, and checks that
// the orphan sweep killed the worker and released the task (attempt kept).
func killMidTask(t *testing.T, d *daemonProc, admin *client.Client) killResult {
	var kr killResult
	var rec runRec
	deadline := time.Now().Add(30 * time.Second)
	for {
		var ok bool
		if rec, ok = d.liveRun(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Error("daemon kill: no running claude to kill within 30s")
			return kr
		}
		time.Sleep(50 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	before, err := admin.GetTask(ctx, rec.ID)
	if err != nil || before.Fence != rec.Fence {
		t.Errorf("daemon kill: task %d before kill: fence %d, err %v", rec.ID, before.Fence, err)
		return kr
	}
	kr.task, kr.fence, kr.attemptsBefore = rec.ID, rec.Fence, before.Attempts
	d.signal(syscall.SIGKILL)
	kr.orphanAliveAfterKill = syscall.Kill(rec.PID, 0) == nil
	d.start()
	for i := 0; i < 400 && syscall.Kill(rec.PID, 0) == nil; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	kr.orphanGoneAfterRestart = syscall.Kill(rec.PID, 0) != nil
	for ctx.Err() == nil {
		after, err := admin.GetTask(ctx, rec.ID)
		if err == nil && (after.Fence > rec.Fence || after.State != "running") {
			kr.fenceAfter, kr.stateAfter, kr.reasonAfter, kr.attemptsAt = after.Fence, after.State, after.Reason, after.Attempts
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	kr.done = true
	switch {
	case !kr.orphanAliveAfterKill:
		t.Error("daemon kill: claude died with the daemon; expected an orphan (own process group)")
	case !kr.orphanGoneAfterRestart:
		t.Error("daemon kill: orphaned claude still alive after restart")
	case kr.fenceAfter == kr.fence && kr.stateAfter == "queued" && kr.attemptsAt != kr.attemptsBefore-1:
		t.Errorf("daemon kill: released task burned an attempt: %+v", kr)
	case kr.fenceAfter == kr.fence+1 && kr.attemptsAt != kr.attemptsBefore:
		t.Errorf("daemon kill: reclaimed task shows %d attempts, want %d (release must not burn one): %+v",
			kr.attemptsAt, kr.attemptsBefore, kr)
	case kr.fenceAfter == kr.fence && kr.stateAfter != "queued":
		t.Errorf("daemon kill: killed attempt reached %s: %+v", kr.stateAfter, kr)
	}
	return kr
}

// prChecks: at most one open PR per task; every done task with a PR URL
// points at an open PR.
func prChecks(t *testing.T, fakeDir string, prURL map[int64]string) (prs, open int) {
	var s ghState
	withGHState(fakeDir, func(st *ghState) { s = *st })
	openByTask := map[string]int{}
	openURL := map[string]bool{}
	for _, p := range s.PRs {
		if p.State == "open" {
			open++
			task, _, _ := strings.Cut(strings.TrimPrefix(p.Head, "agentmesh/task-"), "-f")
			openByTask[task]++
			openURL[p.URL] = true
		}
	}
	for task, n := range openByTask {
		if n > 1 {
			t.Errorf("task %s has %d open PRs", task, n)
		}
	}
	for id, u := range prURL {
		if !openURL[u] {
			t.Errorf("done task %d points at PR %s which is not open", id, u)
		}
	}
	return len(s.PRs), open
}
