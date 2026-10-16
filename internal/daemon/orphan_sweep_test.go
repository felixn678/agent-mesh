package daemon

import (
	"context"
	"os"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
)

// A crash after a task was reported stuck (worktree kept for a human) but
// before its run record was removed must not let the restart sweep delete
// that worktree: the hub refuses the release, so the task was not handed back.
func TestRestartSweepKeepsWorktreeOfFinishedTask(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("untracked")
	d := e.daemon(nil)
	runOne(t, d)
	got := e.task(task.ID)
	if got.State != api.StateStuck || got.Worktree == "" {
		t.Fatalf("setup: want stuck with worktree, got %+v", got)
	}
	n := d.names(task.ID, got.Fence)
	if err := writeJSONFile(n.runRec, runRecord{ID: task.ID, Fence: got.Fence, Repo: "demo",
		Worktree: n.worktree, Branch: n.branch}); err != nil {
		t.Fatal(err)
	}

	e.daemon(nil).sweepOrphans(context.Background())
	if _, err := os.Stat(got.Worktree); err != nil {
		t.Fatalf("sweep removed the worktree kept for a human: %v", err)
	}
	if _, err := os.Stat(n.runRec); !os.IsNotExist(err) {
		t.Fatalf("stale run record left behind: %v", err)
	}
	if after := e.task(task.ID); after.State != api.StateStuck {
		t.Fatalf("task changed by the sweep: %+v", after)
	}
}
