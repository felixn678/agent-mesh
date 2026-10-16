package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

// errCrashSim is returned when a test asks publish to stop as if the daemon
// died right after journaling a step.
var errCrashSim = errors.New("simulated crash")

// pushRetries bounds transient push failures before giving the task back.
const pushRetries = 3

// publish drives a journaled publish from j.Step to the end. Every step
// is journaled (fsynced) before its side effect, and every step is safe to
// repeat, so replay after a crash simply calls publish again.
func (d *Daemon) publish(ctx context.Context, j *journal) error {
	for {
		if err := ctx.Err(); err != nil {
			return err // journal stays for replay
		}
		if d.stopAfterStep != "" && j.Step == d.stopAfterStep {
			return errCrashSim
		}
		var err error
		switch j.Step {
		case stepPrepare:
			err = d.stepPrepare(ctx, j)
		case stepPushing:
			err = d.stepPush(ctx, j)
		case stepPushed:
			err = d.stepAfterPush(ctx, j)
		case stepPRCreating:
			url, perr := d.findOrCreatePR(ctx, j)
			if perr != nil {
				d.log.Warn("draft PR not opened", "task", j.ID, "err", perr)
				j.PublishNote = joinNote(j.PublishNote, notePRFailed)
			}
			j.PRURL = url
			err = d.saveJournal(j, stepPRCreated)
		case stepPRCreated:
			err = d.stepComplete(ctx, j)
		case stepClosingPR:
			err = d.stepClosePR(ctx, j)
		case "":
			return nil // finished
		default:
			return fmt.Errorf("journal task-%d-f%d: unknown step %q", j.ID, j.Fence, j.Step)
		}
		if err != nil {
			return err
		}
	}
}

// stepPrepare is s0 + s1: scans, leftover commit, then a fence check before
// anything leaves the host.
func (d *Daemon) stepPrepare(ctx context.Context, j *journal) error {
	refuse, note, err := d.prepare(ctx, j)
	if err != nil {
		if err := d.fail(ctx, j.ID, j.Fence, true, "publish prepare: "+err.Error(), j.CostUSD, "", j.SessionID); err != nil {
			return err
		}
		return d.endJournal(ctx, j, true)
	}
	if refuse != "" {
		if err := d.fail(ctx, j.ID, j.Fence, false, refuse, j.CostUSD, j.Worktree, j.SessionID); err != nil {
			return err
		}
		return d.endJournal(ctx, j, false)
	}
	j.PublishNote = joinNote(j.PublishNote, note)
	if note == noteNoChanges {
		return d.saveJournal(j, stepPRCreated) // complete with no branch and no PR
	}
	if lost, err := d.fenceCheck(ctx, j.ID, j.Fence); err != nil {
		return err
	} else if lost {
		return d.failAfterLeaseLoss(ctx, j)
	}
	return d.saveJournal(j, stepPushing)
}

// failAfterLeaseLoss runs when the lease is gone but claude's work is
// finished and unpublished. Nothing is pushed. The hub still accepts a late
// fail from the same host+fence when the reaper only marked the task stuck;
// then the worktree is kept for a human. Otherwise (reclaimed) clean up.
func (d *Daemon) failAfterLeaseLoss(ctx context.Context, j *journal) error {
	err := d.report(ctx, "fail", func(ctx context.Context) error {
		_, err := d.hub.Fail(ctx, j.ID, api.FailRequest{Fence: j.Fence, CostUSD: j.CostUSD,
			Reason: "lease lost before publish; work kept unpushed", Worktree: j.Worktree, SessionID: j.SessionID})
		return err
	})
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return d.endJournal(ctx, j, err != nil)
}

// stepPush is s2: push the branch without -u under the repo lock. The empty
// expected value in --force-with-lease means "must not exist yet", so a
// zombie attempt can never overwrite another push.
func (d *Daemon) stepPush(ctx context.Context, j *journal) error {
	lock := d.repoLock(j.Repo)
	var err error
	for i := 0; i < pushRetries; i++ {
		lock.Lock()
		err = d.pushOnce(ctx, j)
		lock.Unlock()
		if err == nil || errors.Is(err, errBranchCollision) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(d.retryBase << i):
		}
	}
	switch {
	case err == nil:
		return d.saveJournal(j, stepPushed)
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, errBranchCollision):
		if err := d.fail(ctx, j.ID, j.Fence, false, "branch collision", j.CostUSD, j.Worktree, j.SessionID); err != nil {
			return err
		}
		return d.endJournal(ctx, j, false)
	}
	if err := d.fail(ctx, j.ID, j.Fence, true, "push: "+err.Error(), j.CostUSD, "", j.SessionID); err != nil {
		return err
	}
	return d.endJournal(ctx, j, true)
}

var errBranchCollision = errors.New("branch collision")

func (d *Daemon) pushOnce(ctx context.Context, j *journal) error {
	head, err := d.wtGit(ctx, j.Repo, j.Worktree, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return err
	}
	remote, err := d.remoteBranchSHA(ctx, j.Repo, j.Worktree, j.Branch)
	if err != nil {
		return err
	}
	switch strings.TrimSpace(remote) {
	case strings.TrimSpace(head):
		return nil // replay: already pushed
	case "":
	default:
		return errBranchCollision
	}
	ref := "refs/heads/" + j.Branch
	_, err = d.wtGit(ctx, j.Repo, j.Worktree, "push", "--quiet", "--no-verify", "--force-with-lease="+ref+":",
		"--end-of-options", "origin", "HEAD:"+ref)
	var ce *cmdError
	if errors.As(err, &ce) && (strings.Contains(ce.Stderr, "stale info") || strings.Contains(ce.Stderr, "already exists")) {
		return errBranchCollision
	}
	return err
}

// stepAfterPush is s3 + s4: fence check, then close open PRs of older
// attempts.
func (d *Daemon) stepAfterPush(ctx context.Context, j *journal) error {
	lost, err := d.fenceCheck(ctx, j.ID, j.Fence)
	if err != nil {
		return err
	}
	if lost {
		// Skip the PR but still try to complete: a late complete is accepted
		// if the task was only reaped to stuck; otherwise the branch stays on
		// origin unreferenced (unique name, harmless).
		j.PublishNote = joinNote(j.PublishNote, noteLeaseLostBeforePR)
		return d.saveJournal(j, stepPRCreated)
	}
	d.supersede(ctx, j)
	return d.saveJournal(j, stepPRCreating)
}
