package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
)

// A worker that rewrites the worktree's .git file must not get the daemon to
// run git (with the GitHub credentials in its env) against a git dir and
// config the worker controls.
func TestWorkerCannotRedirectDaemonGitDir(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("redirectgit")
	runOne(t, e.daemon(nil))
	if b, err := os.ReadFile(filepath.Join(e.fakeDir, "pwned")); err == nil {
		t.Fatalf("daemon git ran the worker's fsmonitor program; env:\n%.300s", b)
	}
	got := e.task(task.ID)
	if got.State != api.StateStuck {
		t.Fatalf("tampered worktree must not publish: %+v", got)
	}
	if len(e.ghState().PRs) != 0 {
		t.Fatal("PR opened from a tampered worktree")
	}
}

// Open PRs from forks that reuse agent-mesh branch names are neither adopted
// as the task's PR nor closed as superseded.
func TestForkPRsWithTaskBranchNamesAreIgnored(t *testing.T) {
	e := newEnv(t)
	withGHState(e.fakeDir, func(s *ghState) {
		s.Next = 2
		s.PRs = []fakePR{
			{Number: 1, Head: "agentmesh/task-1-f1", State: "open", URL: "https://github.com/example/demo/pull/1", Fork: true},
			{Number: 2, Head: "agentmesh/task-1-f0", State: "open", URL: "https://github.com/example/demo/pull/2", Fork: true},
		}
	})
	task := e.addTask("commit")
	runOne(t, e.daemon(nil))
	got := e.task(task.ID)
	if got.State != api.StateDone || got.PRURL == "" || got.PRURL == "https://github.com/example/demo/pull/1" {
		t.Fatalf("fork PR adopted or no PR opened: %+v", got)
	}
	for _, pr := range e.ghState().PRs {
		if pr.Fork && pr.State != "open" {
			t.Fatalf("fork PR %d was closed", pr.Number)
		}
	}
}
