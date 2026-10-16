package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/felixn678/agent-mesh/internal/api"
)

// maxSummaryBytes bounds the result text relayed to the creator session.
const maxSummaryBytes = 2000

// startTask runs one claimed task in its own goroutine.
func (d *Daemon) startTask(ctx context.Context, t api.ClaimedTask) {
	n := d.names(t.ID, t.Fence)
	d.mu.Lock()
	d.running[t.ID] = n.worktree
	d.mu.Unlock()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer func() {
			d.mu.Lock()
			delete(d.running, t.ID)
			d.mu.Unlock()
		}()
		d.execTask(ctx, t, n)
	}()
}

// execTask: worktree → claude → outcome → journaled publish.
func (d *Daemon) execTask(ctx context.Context, t api.ClaimedTask, n taskNames) {
	log := d.log.With("task", t.ID, "fence", t.Fence)
	if err := d.addWorktree(ctx, t, n); err != nil {
		log.Error("worktree setup failed", "err", err)
		if ctx.Err() == nil {
			d.infraFailure(err)
		}
		rctx, cancel := detached()
		defer cancel()
		d.removeWorktree(rctx, t.Repo, n.worktree, n.branch)
		d.release(rctx, t, "setup: "+err.Error())
		return
	}
	log.Info("claude starting", "worktree", n.worktree)
	res, outcome, errTail, err := d.runClaude(ctx, t, n)
	cleanup := func(c context.Context, removeWT bool) {
		if removeWT {
			d.removeWorktree(c, t.Repo, n.worktree, n.branch)
		}
		_ = os.Remove(n.runRec)
	}
	switch outcome {
	case outLeaseLost:
		rctx, cancel := detached()
		defer cancel()
		cleanup(rctx, true)
	case outShutdown:
		rctx, cancel := detached()
		defer cancel()
		cleanup(rctx, true)
		d.release(rctx, t, "daemon shutdown")
	case outTimeout:
		if d.fail(ctx, t.ID, t.Fence, false, "timeout", res.CostUSD, n.worktree, res.SessionID) == nil {
			cleanup(ctx, false)
		}
	case outNoResult:
		reason := "claude exited without a result"
		if err != nil {
			reason += ": " + err.Error()
		}
		if errTail != "" {
			reason += ": " + errTail
		}
		d.infraFailure(errors.New(reason))
		if d.fail(ctx, t.ID, t.Fence, true, reason, res.CostUSD, "", res.SessionID) == nil {
			cleanup(ctx, true)
		}
	case outError:
		d.infraSuccess()
		reason := firstNonEmpty(res.Subtype, "error")
		if d.fail(ctx, t.ID, t.Fence, false, reason, res.CostUSD, n.worktree, res.SessionID) == nil {
			cleanup(ctx, false)
		}
	case outOK:
		d.infraSuccess()
		j := &journal{ID: t.ID, Fence: t.Fence, Repo: t.Repo, Base: t.BaseBranch, Branch: n.branch,
			Worktree: n.worktree, SessionID: res.SessionID, CostUSD: res.CostUSD, Summary: summary(res.Text)}
		if err := d.saveJournal(j, stepPrepare); err != nil {
			log.Error("journal write failed", "err", err)
			_ = d.fail(ctx, t.ID, t.Fence, true, "journal: "+err.Error(), res.CostUSD, "", res.SessionID)
			cleanup(ctx, true)
			return
		}
		if err := d.publish(ctx, j); err != nil {
			log.Warn("publish paused; journal kept for replay", "step", j.Step, "err", err)
		}
	}
}

// summary is the redacted, bounded result text.
func summary(s string) string {
	s = strings.TrimSpace(redact(s))
	if len(s) <= maxSummaryBytes {
		return s
	}
	s = s[:maxSummaryBytes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// replayJournals resumes every pending publish (crash recovery).
func (d *Daemon) replayJournals(ctx context.Context) {
	for _, j := range d.loadJournals() {
		d.mu.Lock()
		d.running[j.ID] = j.Worktree
		d.mu.Unlock()
		d.wg.Add(1)
		go func(j *journal) {
			defer d.wg.Done()
			defer func() {
				d.mu.Lock()
				delete(d.running, j.ID)
				d.mu.Unlock()
			}()
			d.log.Info("replaying publish", "task", j.ID, "fence", j.Fence, "step", j.Step)
			if err := d.publish(ctx, j); err != nil {
				d.log.Warn("replay paused", "task", j.ID, "step", j.Step, "err", fmt.Sprint(err))
			}
		}(j)
	}
}
