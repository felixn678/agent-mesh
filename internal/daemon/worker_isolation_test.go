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
