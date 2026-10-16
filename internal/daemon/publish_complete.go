package daemon

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
)

// stepComplete is s6. Only lease_lost closes the PR: an idempotent replay
// (already_applied) and a cancel leave it open.
func (d *Daemon) stepComplete(ctx context.Context, j *journal) error {
	branch := j.Branch
	if j.PublishNote == noteNoChanges || strings.Contains(j.PublishNote, noteNoChanges) {
		branch = ""
	}
	err := d.report(ctx, "complete", func(ctx context.Context) error {
		_, err := d.hub.Complete(ctx, j.ID, api.CompleteRequest{Fence: j.Fence, Branch: branch, PRURL: j.PRURL,
			SessionID: j.SessionID, CostUSD: j.CostUSD, PublishNote: j.PublishNote, Summary: j.Summary})
		return err
	})
	var le *client.LeaseError
	switch {
	case err == nil:
		d.log.Info("task done", "task", j.ID, "fence", j.Fence, "pr", j.PRURL)
		return d.endJournal(ctx, j, true)
	case errors.As(err, &le) && le.Code == api.CodeLeaseLost && j.PRURL != "":
		return d.saveJournal(j, stepClosingPR)
	case errors.As(err, &le):
		d.log.Warn("complete refused; PR left as is", "task", j.ID, "code", le.Code, "pr", j.PRURL)
		return d.endJournal(ctx, j, true)
	case ctx.Err() != nil:
		return err
	}
	d.log.Error("complete rejected; worktree kept", "task", j.ID, "err", err)
	return d.endJournal(ctx, j, false)
}

// stepClosePR closes this attempt's PR after a lease loss, retrying with
// backoff until gh succeeds or ctx ends (the journal keeps the step).
func (d *Daemon) stepClosePR(ctx context.Context, j *journal) error {
	delay := d.retryBase
	for {
		err := d.closePR(ctx, j.Worktree, j.PRURL, "Superseded by a newer attempt; this attempt lost its lease.")
		if err == nil {
			return d.endJournal(ctx, j, true)
		}
		d.log.Warn("closing PR failed; retrying", "task", j.ID, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, 5*time.Minute)
	}
}

// endJournal finishes a publish: optionally removes the worktree + branch,
// then deletes the journal and run record.
func (d *Daemon) endJournal(ctx context.Context, j *journal, removeWT bool) error {
	d.finishPublish(ctx, j, removeWT)
	j.Step = ""
	return nil
}

func (d *Daemon) finishPublish(ctx context.Context, j *journal, removeWT bool) {
	if removeWT {
		d.removeWorktree(ctx, j.Repo, j.Worktree, j.Branch)
	}
	n := d.names(j.ID, j.Fence)
	_ = os.Remove(n.journal)
	_ = os.Remove(n.runRec)
}

func joinNote(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "," + b
}
