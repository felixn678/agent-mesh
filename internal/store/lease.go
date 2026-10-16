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
