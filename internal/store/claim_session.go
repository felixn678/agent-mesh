package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/felixn678/agent-mesh/internal/api"
)

// sessionEligible selects queued (or lease-expired) tasks that an alive
// session of :host may take: same project as the session, and targeting
// either unset or pointing at this host / this session. The session row is
// part of the join, so liveness, host ownership and repo are checked in the
// same statement as the claim itself.
const sessionEligible = `FROM tasks t JOIN sessions s
  ON s.id = :sid AND s.host = :host AND s.ended_at IS NULL AND s.last_seen_at >= :cut
 AND s.repo <> '' AND t.repo = s.repo AND s.worker = 1
WHERE (t.state = 'queued' OR (t.state = 'running' AND t.lease_expires_at < :now))
  AND t.attempts < t.max_attempts
  AND (t.host IS NULL OR t.host = :host)
  AND (t.target_host IS NULL OR t.target_host = :host)
  AND (t.target_session IS NULL OR t.target_session = :sid)`

const sessionClaimSQL = `
UPDATE tasks SET state='running', claimed_by=:holder, fence=fence+1, attempts=attempts+1,
  claim_request_id=:rid, lease_expires_at=:now+:ttl, last_heartbeat_at=:now, updated_at=:now,
  reason = CASE WHEN state='running' THEN 'lease expired on '||claimed_by ELSE reason END
WHERE id = (SELECT t.id ` + sessionEligible + ` ORDER BY t.id LIMIT 1)
RETURNING id, fence, repo, base_branch, prompt, permission_mode, tool_profile, max_budget_usd`

// SessionHolder checks that session id exists on ownerHost ("" = any, admin)
// and returns its lease holder name, "session:<id>". The returned holder is
// what Heartbeat, Complete, Fail and Release take for session-held tasks, so
// one session can never act on a task another holder claimed.
func (s *Store) SessionHolder(ctx context.Context, ownerHost, id string) (string, error) {
	if !api.ValidSessionID(id) {
		return "", &api.ValidationError{Msg: "invalid session id"}
	}
	if _, err := s.ownedSession(ctx, ownerHost, id); err != nil {
		return "", err
	}
	return SessionHolderPrefix + id, nil
}

func (s *Store) sessionArgs(host, id string) []any {
	return []any{sql.Named("sid", id), sql.Named("host", host),
		sql.Named("cut", s.aliveCutoff()), sql.Named("now", s.ms())}
}

// ClaimForSession atomically hands the oldest eligible task to an alive
// session registered on host, or (nil, nil) when none fits. Errors:
// ErrSessionNotFound, ErrSessionOwner (session on another host) and
// ErrSessionEnded (ended or not seen within the alive window).
func (s *Store) ClaimForSession(ctx context.Context, host, id string) (*api.ClaimedTask, error) {
	return s.ClaimForSessionRequest(ctx, host, id, api.SessionClaimRequest{})
}

// ClaimForSessionRequest is ClaimForSession with an optional request id: a
// retried claim with the same id returns the task the session already holds.
func (s *Store) ClaimForSessionRequest(ctx context.Context, host, id string, req api.SessionClaimRequest) (*api.ClaimedTask, error) {
	if !api.ValidSessionID(id) {
		return nil, &api.ValidationError{Msg: "invalid session id"}
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	c, err := s.claimOnce(ctx, SessionHolderPrefix+id, req.RequestID, func(q querier) (*api.ClaimedTask, error) {
		return s.sessionClaim(ctx, q, host, id, req.RequestID)
	})
	if err != nil || c != nil {
		return c, err
	}
	// No row: either nothing to claim, or the session is unusable.
	sess, err := s.ownedSession(ctx, host, id)
	if err != nil {
		return nil, err
	}
	if !sess.Alive {
		return nil, ErrSessionEnded
	}
	if !sess.Worker {
		return nil, ErrNotWorker
	}
	return nil, nil
}

func (s *Store) sessionClaim(ctx context.Context, q querier, host, id, rid string) (*api.ClaimedTask, error) {
	args := append(s.sessionArgs(host, id), sql.Named("rid", nullable(rid)),
		sql.Named("holder", SessionHolderPrefix+id), sql.Named("ttl", s.cfg.ttlFor(SessionHolderPrefix).Milliseconds()))
	var c api.ClaimedTask
	err := q.QueryRowContext(ctx, sessionClaimSQL, args...).
		Scan(&c.ID, &c.Fence, &c.Repo, &c.BaseBranch, &c.Prompt, &c.PermissionMode, &c.ToolProfile, &c.MaxBudgetUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session claim: %w", err)
	}
	return &c, nil
}

// ClaimableCount returns how many tasks the session could claim right now
// without claiming any (0 for unknown, ended or repo-less sessions).
func (s *Store) ClaimableCount(ctx context.Context, id string) (int, error) {
	var host string
	err := s.db.QueryRowContext(ctx, "SELECT host FROM sessions WHERE id=?", id).Scan(&host)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("claimable count: %w", err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) "+sessionEligible, s.sessionArgs(host, id)...).Scan(&n); err != nil {
		return 0, fmt.Errorf("claimable count: %w", err)
	}
	return n, nil
}

// AddTask enqueues a task on behalf of principal p. When the spec names a
// creator session it must exist and belong to p's host (admin: any), so a
// token cannot make the hub message someone else's session.
func (s *Store) AddTask(ctx context.Context, spec api.TaskSpec, p api.Principal) (api.Task, error) {
	t, _, err := s.AddTaskReplay(ctx, spec, p)
	return t, err
}

// AddTaskReplay is AddTask that also reports whether spec.RequestID matched
// a task this principal added before (returned instead of a new one).
func (s *Store) AddTaskReplay(ctx context.Context, spec api.TaskSpec, p api.Principal) (api.Task, bool, error) {
	if spec.CreatedBySession != "" {
		owner := p.Host
		if p.Admin {
			owner = ""
		}
		if _, err := s.SessionHolder(ctx, owner, spec.CreatedBySession); err != nil {
			return api.Task{}, false, err
		}
	}
	// A task for a session that opted out of worker mode could never be
	// claimed; refuse it up front (an unknown target may still register).
	if spec.TargetSession != "" {
		if target, err := s.getSession(ctx, spec.TargetSession); err == nil && !target.Worker {
			return api.Task{}, false, ErrNotWorker
		}
	}
	return s.add(ctx, spec, p.CreatedBy())
}
