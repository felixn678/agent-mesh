package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

func TestHappyPathOpensDraftPRAndCleansUp(t *testing.T) {
	e := newEnv(t)
	d := e.daemon(nil)
	task := e.addTask("commit add a file")
	runOne(t, d)

	got := e.task(task.ID)
	if got.State != api.StateDone || got.Branch != "agentmesh/task-1-f1" || got.PRURL == "" {
		t.Fatalf("task = %+v", got)
	}
	if got.SessionID != fakeSessionID || got.CostUSD != 0.25 || got.Summary != fakeResultText {
		t.Fatalf("session/cost/summary = %q %v %q", got.SessionID, got.CostUSD, got.Summary)
	}
	if !strings.Contains(e.remoteBranches(), "agentmesh/task-1-f1") {
		t.Fatalf("branch not pushed: %s", e.remoteBranches())
	}
	gh := e.ghState()
	if len(gh.PRs) != 1 || gh.PRs[0].State != "open" || gh.PRs[0].Base != "main" {
		t.Fatalf("PRs = %+v", gh.PRs)
	}
	body := gh.PRs[0].Body
	if strings.Contains(body, fakeResultText) || !strings.Contains(body, "Task: #1") || !strings.Contains(body, "fake: commit") {
		t.Fatalf("PR body must be templated (ids + commit subjects, no model text):\n%s", body)
	}
	for _, sub := range []string{"worktrees", "spool", "run"} {
		if left := dirEntries(t, filepath.Join(e.cfg.StateDir, sub)); len(left) != 0 {
			t.Fatalf("%s not cleaned: %v", sub, left)
		}
	}
	argv, _ := os.ReadFile(filepath.Join(e.fakeDir, "claude.argv"))
	for _, want := range []string{"--strict-mcp-config", "--setting-sources\nproject", "--permission-prompts\nnone",
		"Bash(git push*)", "Bash(gh *)", "--tools\nRead\nEdit\nWrite\nBash\n--allowedTools"} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("argv missing %q:\n%s", want, argv)
		}
	}
	env, _ := os.ReadFile(filepath.Join(e.fakeDir, "claude.env"))
	if string(env) != "GH_TOKEN=\nAGENTMESH_HOOKS=off\n" {
		t.Errorf("worker env = %q; want GH_TOKEN stripped and hooks off", env)
	}
}

func TestNoChangesCompletesWithoutPR(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("nochange")
	runOne(t, e.daemon(nil))
	got := e.task(task.ID)
	if got.State != api.StateDone || got.PublishNote != noteNoChanges || got.PRURL != "" || got.Branch != "" {
		t.Fatalf("task = %+v", got)
	}
	if len(e.ghState().PRs) != 0 {
		t.Fatal("no PR expected")
	}
}

func TestLeftoverTrackedChangesAreCommitted(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("dirty")
	runOne(t, e.daemon(nil))
	got := e.task(task.ID)
	if got.State != api.StateDone || got.PublishNote != noteLeftover || got.PRURL == "" {
		t.Fatalf("task = %+v", got)
	}
}

func TestPublishRefusalsLeaveTaskStuckWithWorktree(t *testing.T) {
	cases := []struct{ prompt, reason string }{
		{"untracked", "untracked files left: stray.txt"},
		{"sensitive", "sensitive path: .mcp.json"},
		{"secret", "secret scan hit"},
		{"error", "error_max_budget_usd"},
	}
	for _, c := range cases {
		t.Run(c.prompt, func(t *testing.T) {
			e := newEnv(t)
			if c.prompt == "secret" && !e.realGL {
				t.Skip("gitleaks not installed")
			}
			task := e.addTask(c.prompt)
			runOne(t, e.daemon(nil))
			got := e.task(task.ID)
			if got.State != api.StateStuck || got.Reason != c.reason {
				t.Fatalf("state=%s reason=%q; want stuck %q", got.State, got.Reason, c.reason)
			}
			if got.Worktree == "" || got.SessionID != fakeSessionID {
				t.Fatalf("stuck task should keep worktree and session id: %+v", got)
			}
			if _, err := os.Stat(got.Worktree); err != nil {
				t.Fatalf("worktree removed: %v", err)
			}
			if strings.Contains(e.remoteBranches(), "agentmesh/") || len(e.ghState().PRs) != 0 {
				t.Fatal("nothing may be published")
			}
		})
	}
}

func TestBranchCollisionIsNotRetried(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("commit")
	// Someone else already owns the branch name on origin.
	main := gitRun(t, e.origin, "rev-parse", "refs/heads/main")
	gitRun(t, e.origin, "update-ref", "refs/heads/agentmesh/task-1-f1", main)
	runOne(t, e.daemon(nil))
	got := e.task(task.ID)
	if got.State != api.StateStuck || got.Reason != "branch collision" {
		t.Fatalf("task = %+v", got)
	}
	if gitRun(t, e.origin, "rev-parse", "refs/heads/agentmesh/task-1-f1") != main {
		t.Fatal("existing branch was overwritten")
	}
}

func TestConcurrentTasksShareOneClone(t *testing.T) {
	e := newEnv(t)
	e.cfg.MaxSlots = 4
	d := e.daemon(nil)
	for i := 0; i < 4; i++ {
		e.addTask("commit parallel")
	}
	for i := 0; i < 4; i++ {
		d.tick(t.Context())
	}
	waitTasks(t, d)
	for id := int64(1); id <= 4; id++ {
		if got := e.task(id); got.State != api.StateDone || got.PRURL == "" {
			t.Fatalf("task %d = %+v", id, got)
		}
	}
	if n := len(e.ghState().PRs); n != 4 {
		t.Fatalf("PRs = %d", n)
	}
}

func TestTaskNeedingUnknownProfileIsNeverClaimed(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("commit", func(s *api.TaskSpec) { s.ToolProfile = "gpu" })
	d := e.daemon(nil)
	runOne(t, d)
	runOne(t, d)
	if got := e.task(task.ID); got.State != api.StateQueued || got.Attempts != 0 {
		t.Fatalf("task = %+v", got)
	}
}

func TestWallClockTimeoutKillsAndSticks(t *testing.T) {
	e := newEnv(t)
	e.cfg.TaskTimeout = 300 * time.Millisecond
	task := e.addTask("sleep")
	runOne(t, e.daemon(nil))
	got := e.task(task.ID)
	if got.State != api.StateStuck || got.Reason != "timeout" {
		t.Fatalf("task = %+v", got)
	}
	assertDead(t, e.fakeDir)
}
