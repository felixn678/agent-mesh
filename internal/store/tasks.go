package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

// ListLimit caps List results; ReportLimit caps ListForReport.
const (
	ListLimit   = 500
	ReportLimit = 1000
)

// taskCols is the column list scanned by scanTask; NULLs become zero values.
const taskCols = `id, repo, base_branch, prompt, permission_mode, tool_profile, max_budget_usd,
 COALESCE(host,''), state, COALESCE(claimed_by,''), fence, attempts, max_attempts,
 COALESCE(lease_expires_at,0), COALESCE(last_heartbeat_at,0), COALESCE(branch,''),
 COALESCE(pr_url,''), COALESCE(session_id,''), COALESCE(worktree,''), cost_usd,
 COALESCE(publish_note,''), COALESCE(reason,''), COALESCE(created_by,''),
 created_at, updated_at, COALESCE(finished_at,0)`

type scanner interface{ Scan(dest ...any) error }

func scanTask(r scanner) (api.Task, error) {
	var t api.Task
	err := r.Scan(&t.ID, &t.Repo, &t.BaseBranch, &t.Prompt, &t.PermissionMode, &t.ToolProfile,
		&t.MaxBudgetUSD, &t.Host, &t.State, &t.ClaimedBy, &t.Fence, &t.Attempts, &t.MaxAttempts,
		&t.LeaseExpiresAt, &t.LastHeartbeat, &t.Branch, &t.PRURL, &t.SessionID, &t.Worktree,
		&t.CostUSD, &t.PublishNote, &t.Reason, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt, &t.FinishedAt)
	return t, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Add validates the spec and enqueues it.
func (s *Store) Add(ctx context.Context, spec api.TaskSpec, createdBy string) (api.Task, error) {
	spec = spec.WithDefaults()
	if err := spec.Validate(); err != nil {
		return api.Task{}, err
	}
	now := s.ms()
	row := s.db.QueryRowContext(ctx, `INSERT INTO tasks
	  (repo, base_branch, prompt, permission_mode, tool_profile, max_budget_usd, host, max_attempts,
	   created_by, created_at, updated_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?,?) RETURNING `+taskCols,
		spec.Repo, spec.BaseBranch, spec.Prompt, spec.PermissionMode, spec.ToolProfile,
		spec.MaxBudgetUSD, nullable(spec.Host), spec.MaxAttempts, nullable(createdBy), now, now)
	t, err := scanTask(row)
	if err != nil {
		return api.Task{}, fmt.Errorf("insert task: %w", err)
	}
	return t, nil
}

// Get returns one task or ErrNotFound.
func (s *Store) Get(ctx context.Context, id int64) (api.Task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, "SELECT "+taskCols+" FROM tasks WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return api.Task{}, ErrNotFound
	}
	if err != nil {
		return api.Task{}, fmt.Errorf("get task: %w", err)
	}
	return t, nil
}

func (s *Store) query(ctx context.Context, where string, limit int, args ...any) ([]api.Task, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+taskCols+" FROM tasks WHERE "+where+" ORDER BY id DESC LIMIT "+fmt.Sprint(limit), args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	out := []api.Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// List returns up to ListLimit tasks, newest first, optionally filtered by
// state ("" = any) and updated at or after since (zero = any).
func (s *Store) List(ctx context.Context, state string, since time.Time) ([]api.Task, error) {
	return s.query(ctx, "(?1 = '' OR state = ?1) AND updated_at >= ?2", ListLimit, state, sinceMS(since))
}

// ListForReport returns tasks updated at or after since plus every task that
// is not finished (queued, running, stuck), newest first, capped at ReportLimit.
// REST and MCP both build the morning report from this.
func (s *Store) ListForReport(ctx context.Context, since time.Time) ([]api.Task, error) {
	return s.query(ctx, "updated_at >= ?1 OR state IN ('queued','running','stuck')", ReportLimit, sinceMS(since))
}

func sinceMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// transition applies a guarded single-statement state change and returns the
// updated task; zero rows distinguishes missing from wrong-state.
func (s *Store) transition(ctx context.Context, id int64, set, guard string) (api.Task, error) {
	now := s.ms()
	t, err := scanTask(s.db.QueryRowContext(ctx,
		"UPDATE tasks SET "+set+", updated_at=?2 WHERE id=?1 AND "+guard+" RETURNING "+taskCols, id, now))
	if err == nil {
		return t, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return api.Task{}, fmt.Errorf("update task: %w", err)
	}
	if _, gerr := s.Get(ctx, id); gerr != nil {
		return api.Task{}, gerr
	}
	return api.Task{}, ErrConflict
}

// Cancel stops a queued, running or stuck task. A running worker learns of it
// through a 409 "cancelled" on its next heartbeat. Fence and claimed_by are
// kept so that response can be attributed to the right holder.
func (s *Store) Cancel(ctx context.Context, id int64) (api.Task, error) {
	return s.transition(ctx, id,
		"state='cancelled', lease_expires_at=NULL, finished_at=?2, reason='cancelled by admin'",
		"state IN ('queued','running','stuck')")
}

// Requeue moves a stuck or cancelled task back to queued with attempts reset.
// The fence is untouched; the next claim bumps it.
func (s *Store) Requeue(ctx context.Context, id int64) (api.Task, error) {
	return s.transition(ctx, id,
		"state='queued', attempts=0, lease_expires_at=NULL, finished_at=NULL, reason=NULL",
		"state IN ('stuck','cancelled')")
}
