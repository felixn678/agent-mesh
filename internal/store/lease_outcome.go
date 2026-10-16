package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/felixn678/agent-mesh/internal/api"
)

// Ending an attempt: complete, fail and release share one fenced, idempotent
// update path (finish), with classify explaining refusals.

// reaperStuck reports whether a stuck task still carries the lease marker the
// reaper leaves (the holder's own Complete/Fail clears it).
func (s *Store) reaperStuck(ctx context.Context, id int64) bool {
	var marked bool
	err := s.db.QueryRowContext(ctx, "SELECT lease_expires_at IS NOT NULL FROM tasks WHERE id=? AND state='stuck'", id).Scan(&marked)
	return err == nil && marked
}

// classify explains why a fenced update matched zero rows. It returns the
// task state when the caller still owns (fence+host) the task, so callers can
// detect an already-applied outcome; ErrCancelled when the owner's task was
// cancelled; ErrLeaseLost when ownership moved; ErrNotFound for unknown ids.
func (s *Store) classify(ctx context.Context, host string, id, fence int64) (string, error) {
	var state, claimedBy string
	var curFence int64
	err := s.db.QueryRowContext(ctx, "SELECT state, fence, COALESCE(claimed_by,'') FROM tasks WHERE id=?", id).
		Scan(&state, &curFence, &claimedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("classify: %w", err)
	}
	if curFence != fence || claimedBy != host {
		return "", ErrLeaseLost
	}
	if state == api.StateCancelled {
		return "", ErrCancelled
	}
	return state, nil
}

// finish runs a fenced terminal/handback update. okStates are the states that
// mean "this exact outcome was already recorded", making replays idempotent.
// With afterReap the owner may also finish a task the reaper marked stuck at
// the attempt cap (recognisable by its kept lease_expires_at): the fence never
// moved, so nobody else can hold it, and the worker's real outcome wins. Every
// set clause clears lease_expires_at, so a replay lands in okStates.
func (s *Store) finish(ctx context.Context, host string, id, fence int64, afterReap bool, set string, okStates []string,
	args ...any) (alreadyApplied bool, err error) {
	named := append([]any{sql.Named("id", id), sql.Named("fence", fence), sql.Named("host", host),
		sql.Named("now", s.ms()), sql.Named("after_reap", afterReap)}, args...)
	res, err := s.db.ExecContext(ctx, "UPDATE tasks SET "+set+", updated_at=:now "+
		"WHERE id=:id AND fence=:fence AND claimed_by=:host AND (state='running' OR "+
		"(:after_reap AND state='stuck' AND lease_expires_at IS NOT NULL))", named...)
	if err != nil {
		return false, fmt.Errorf("update lease: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.notify.signalAll() // creator notice may have been inserted; requeues are claimable
		return false, nil
	}
	state, err := s.classify(ctx, host, id, fence)
	if err != nil {
		return false, err
	}
	for _, ok := range okStates {
		if state == ok {
			return true, nil
		}
	}
	return false, ErrLeaseLost
}

// Complete records success. Replaying it with the same fence/host is a no-op
// reported as alreadyApplied.
func (s *Store) Complete(ctx context.Context, host string, id int64, r api.CompleteRequest) (bool, error) {
	if err := r.Validate(); err != nil {
		return false, err
	}
	return s.finish(ctx, host, id, r.Fence, true,
		`state='done', branch=:branch, pr_url=:pr, session_id=:sess, cost_usd=cost_usd+:cost,
		 publish_note=:note, summary=:summary, lease_expires_at=NULL, finished_at=:now`,
		[]string{api.StateDone},
		sql.Named("branch", nullable(r.Branch)), sql.Named("pr", nullable(r.PRURL)),
		sql.Named("sess", nullable(r.SessionID)), sql.Named("cost", r.CostUSD), sql.Named("note", nullable(r.PublishNote)), sql.Named("summary", nullable(r.Summary)))
}

// Fail ends the attempt. With Retry and attempts left the task is requeued,
// otherwise it becomes stuck.
func (s *Store) Fail(ctx context.Context, host string, id int64, r api.FailRequest) (bool, error) {
	if err := r.Validate(); err != nil {
		return false, err
	}
	ok := []string{api.StateStuck}
	if r.Retry {
		ok = append(ok, api.StateQueued)
	}
	return s.finish(ctx, host, id, r.Fence, true,
		`state=CASE WHEN :retry AND attempts<max_attempts THEN 'queued' ELSE 'stuck' END,
		 finished_at=CASE WHEN :retry AND attempts<max_attempts THEN NULL ELSE :now END,
		 cost_usd=cost_usd+:cost, reason=:reason, worktree=:wt, session_id=COALESCE(NULLIF(:sess,''), session_id),
		 lease_expires_at=NULL`,
		ok, sql.Named("retry", r.Retry), sql.Named("cost", r.CostUSD),
		sql.Named("reason", nullable(r.Reason)), sql.Named("wt", nullable(r.Worktree)), sql.Named("sess", r.SessionID))
}

// Release hands the task back to the queue without consuming an attempt.
func (s *Store) Release(ctx context.Context, host string, id int64, r api.ReleaseRequest) (bool, error) {
	if err := r.Validate(); err != nil {
		return false, err
	}
	return s.finish(ctx, host, id, r.Fence, false,
		`state='queued', attempts=attempts-1, reason=:reason, lease_expires_at=NULL`,
		[]string{api.StateQueued},
		sql.Named("reason", "released by "+host+": "+r.Reason))
}
