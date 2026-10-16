package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/felixn678/agent-mesh/internal/api"
)

// claimSQL is one statement, so selection and the fence bump are atomic: two
// claimers can never receive the same (id, fence). Expired running leases are
// reclaimable immediately. Right-hand sides see pre-update column values.
const claimSQL = `
UPDATE tasks SET state='running', claimed_by=:host, fence=fence+1, attempts=attempts+1,
  claim_request_id=:rid, lease_expires_at=:now+:ttl, last_heartbeat_at=:now, updated_at=:now,
  reason = CASE WHEN state='running' THEN 'lease expired on '||claimed_by ELSE reason END
WHERE id = (SELECT id FROM tasks
  WHERE (state='queued' OR (state='running' AND lease_expires_at < :now))
    AND attempts < max_attempts
    AND repo            IN (SELECT value FROM json_each(:repos))
    AND permission_mode IN (SELECT value FROM json_each(:modes))
    AND tool_profile    IN (SELECT value FROM json_each(:profiles))
    AND max_budget_usd <= :budget_cap
    AND (host IS NULL OR host = :host)
    AND (target_host IS NULL OR target_host = :host)
    AND target_session IS NULL
  ORDER BY id LIMIT 1)
RETURNING id, fence, repo, base_branch, prompt, permission_mode, tool_profile, max_budget_usd`

func jsonList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v) // []string cannot fail to marshal
	return string(b)
}

// Claim hands the oldest eligible task to host, or (nil, nil) when none fits.
// With req.RequestID, a retry of a claim whose response was lost returns the
// task host already holds under that id (same fence) instead of a new one.
func (s *Store) Claim(ctx context.Context, host string, req api.ClaimRequest) (*api.ClaimedTask, error) {
	return s.claimOnce(ctx, host, req.RequestID, func(q querier) (*api.ClaimedTask, error) {
		return s.claim(ctx, q, host, req)
	})
}

func (s *Store) claim(ctx context.Context, q querier, host string, req api.ClaimRequest) (*api.ClaimedTask, error) {
	now := s.ms()
	repos := make([]string, 0, len(req.Repos))
	for _, r := range req.Repos { // same normalization as stored task repos
		if c, err := api.CanonicalRepo(r); err == nil {
			repos = append(repos, c)
		}
	}
	var c api.ClaimedTask
	err := q.QueryRowContext(ctx, claimSQL,
		sql.Named("host", host), sql.Named("rid", nullable(req.RequestID)), sql.Named("now", now), sql.Named("ttl", s.cfg.ttlFor(host).Milliseconds()),
		sql.Named("repos", jsonList(repos)), sql.Named("modes", jsonList(req.Modes)),
		sql.Named("profiles", jsonList(req.Profiles)), sql.Named("budget_cap", req.BudgetCap),
	).Scan(&c.ID, &c.Fence, &c.Repo, &c.BaseBranch, &c.Prompt, &c.PermissionMode, &c.ToolProfile, &c.MaxBudgetUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	return &c, nil
}

// Heartbeat extends the lease. The fence is the authority; an expired lease
// nobody reclaimed may be revived only within ReviveWindow.
func (s *Store) Heartbeat(ctx context.Context, host string, id, fence int64) error {
	now := s.ms()
	var got int64
	err := s.db.QueryRowContext(ctx, `UPDATE tasks SET lease_expires_at=:now+:ttl, last_heartbeat_at=:now, updated_at=:now
	  WHERE id=:id AND fence=:fence AND claimed_by=:host AND state='running'
	    AND lease_expires_at >= :now - :revive RETURNING id`,
		sql.Named("now", now), sql.Named("ttl", s.cfg.ttlFor(host).Milliseconds()), sql.Named("id", id),
		sql.Named("fence", fence), sql.Named("host", host), sql.Named("revive", s.cfg.ReviveWindow.Milliseconds()),
	).Scan(&got)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("heartbeat: %w", err)
	}
	state, err := s.classify(ctx, host, id, fence)
	if err != nil {
		return err
	}
	if state == api.StateStuck && s.reaperStuck(ctx, id) {
		return ErrLeaseReaped
	}
	return ErrLeaseLost // holder does not own a live lease
}

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
