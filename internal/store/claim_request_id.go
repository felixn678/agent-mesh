package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/felixn678/agent-mesh/internal/api"
)

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// claimReplaySQL finds the task holder still holds from an earlier claim
// carrying the same request id.
const claimReplaySQL = `SELECT id, fence, repo, base_branch, prompt, permission_mode, tool_profile, max_budget_usd
  FROM tasks WHERE claimed_by=? AND claim_request_id=? AND state='running' ORDER BY id LIMIT 1`

// claimOnce runs claim, first replaying an earlier claim by holder with the
// same request id. Lookup and claim share one transaction, so concurrent
// retries of one request cannot both claim. Without a request id it is a
// plain single-statement claim.
func (s *Store) claimOnce(ctx context.Context, holder, requestID string,
	claim func(q querier) (*api.ClaimedTask, error)) (*api.ClaimedTask, error) {
	if requestID == "" {
		return claim(s.db)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var c api.ClaimedTask
	err = tx.QueryRowContext(ctx, claimReplaySQL, holder, requestID).
		Scan(&c.ID, &c.Fence, &c.Repo, &c.BaseBranch, &c.Prompt, &c.PermissionMode, &c.ToolProfile, &c.MaxBudgetUSD)
	var got *api.ClaimedTask
	switch {
	case err == nil:
		got = &c
	case errors.Is(err, sql.ErrNoRows):
		if got, err = claim(tx); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("claim replay: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	return got, nil
}
