package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/shirou/gopsutil/v4/process"
)

// sweepOrphans runs at startup, before journal replay: for every run record
// left by a previous daemon, kill its claude process group if it is still
// alive, then hand the task back (release keeps the attempt). A run whose
// journal exists finished claude already; replay owns its worktree.
func (d *Daemon) sweepOrphans(ctx context.Context) {
	ents, _ := os.ReadDir(d.dir("run"))
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(d.dir("run"), e.Name())
		var rec runRecord
		if err := readJSONFile(path, &rec); err != nil || rec.ID < 1 || rec.Fence < 1 {
			d.log.Error("unreadable run record removed", "file", e.Name(), "err", err)
			_ = os.Remove(path)
			continue
		}
		if rec.PID > 0 && d.isOurClaude(ctx, rec.PID) {
			d.log.Warn("killing orphaned claude", "task", rec.ID, "pid", rec.PID)
			d.killOrphan(rec.PID)
		}
		n := d.names(rec.ID, rec.Fence)
		if _, err := os.Stat(n.journal); err == nil {
			_ = os.Remove(path)
			continue
		}
		t := api.ClaimedTask{ID: rec.ID, Fence: rec.Fence, Repo: rec.Repo}
		// Only a task we actually handed back loses its worktree. If the hub
		// refused (the attempt already ended, e.g. stuck with the worktree
		// kept for a human) or is unreachable, leave it for gc.
		if d.release(ctx, t, "daemon restart") == nil {
			d.removeWorktree(ctx, rec.Repo, n.worktree, n.branch)
		}
		_ = os.Remove(path)
		_ = os.Remove(n.prompt)
	}
}

// isOurClaude checks that pid is alive and its command line runs the
// configured claude binary (pid reuse guard).
func (d *Daemon) isOurClaude(ctx context.Context, pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	p, err := process.NewProcessWithContext(ctx, int32(pid))
	if err != nil {
		return false
	}
	args, err := p.CmdlineSliceWithContext(ctx)
	if err != nil {
		return false
	}
	want := filepath.Base(d.cfg.ClaudeBin)
	for _, a := range args {
		if filepath.Base(a) == want {
			return true
		}
	}
	return false
}

// killOrphan TERMs the group, waits up to killGrace for the leader to go,
// then KILLs the group.
func (d *Daemon) killOrphan(pgid int) {
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	deadline := time.Now().Add(d.killGrace)
	for time.Now().Before(deadline) && syscall.Kill(pgid, 0) == nil {
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
