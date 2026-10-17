package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/felixn678/agent-mesh/internal/api"
)

// Errors specific to sessions and messages.
var (
	ErrSessionNotFound = errors.New("session not found")
	ErrSessionOwner    = errors.New("session belongs to another host")
	ErrSessionEnded    = errors.New("session has ended")
	ErrInboxFull       = errors.New("recipient inbox is full")
)

// ownerHost parameters: "" means unrestricted (admin); otherwise the session
// must belong to that host.

// RegisterSession creates or refreshes a session (it doubles as a heartbeat
// and revives an ended session). host comes from the token. A session id that
// already belongs to another host yields ErrSessionOwner.
func (s *Store) RegisterSession(ctx context.Context, host string, r api.RegisterSessionRequest) (api.Session, error) {
	if err := r.Validate(); err != nil {
		return api.Session{}, err
	}
	repo := ""
	if r.Repo != "" {
		repo, _ = api.NormalizeRepo(r.Repo) // Validate accepted it
	}
	now := s.ms()
	var worker any // nil keeps the stored flag (new rows: 1)
	if r.Worker != nil {
		worker = *r.Worker
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO sessions (id, host, cwd, name, repo, started_at, last_seen_at, worker, agent_version)
	  VALUES (?1,?2,?3,?4,?6,?5,?5,COALESCE(?7,1),?8)
	  ON CONFLICT(id) DO UPDATE SET cwd=excluded.cwd, name=excluded.name, last_seen_at=excluded.last_seen_at,
	    repo=CASE WHEN excluded.repo <> '' THEN excluded.repo ELSE sessions.repo END,
	    worker=COALESCE(?7, sessions.worker),
	    agent_version=CASE WHEN excluded.agent_version <> '' THEN excluded.agent_version ELSE sessions.agent_version END,
	    ended_at=NULL, started_at=CASE WHEN ended_at IS NULL THEN started_at ELSE excluded.started_at END
	  WHERE sessions.host = excluded.host`, r.ID, host, r.Cwd, r.Name, now, repo, worker, r.AgentVersion)
	if err != nil {
		return api.Session{}, fmt.Errorf("register session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return api.Session{}, ErrSessionOwner
	}
	if r.Machine != nil {
		if err := s.upsertMachine(ctx, host, *r.Machine); err != nil {
			return api.Session{}, err
		}
	}
	return s.getSession(ctx, r.ID)
}

const sessionCols = `id, host, cwd, name, repo, worker, agent_version, started_at, last_seen_at, COALESCE(ended_at,0),
  (ended_at IS NULL AND last_seen_at >= ?1)`

func scanSession(r scanner) (api.Session, error) {
	var x api.Session
	err := r.Scan(&x.ID, &x.Host, &x.Cwd, &x.Name, &x.Repo, &x.Worker, &x.AgentVersion, &x.StartedAt, &x.LastSeenAt, &x.EndedAt, &x.Alive)
	return x, err
}

func (s *Store) aliveCutoff() int64 { return s.ms() - s.cfg.sessionAlive().Milliseconds() }

func (s *Store) getSession(ctx context.Context, id string) (api.Session, error) {
	x, err := scanSession(s.db.QueryRowContext(ctx,
		"SELECT "+sessionCols+" FROM sessions WHERE id=?2", s.aliveCutoff(), id))
	if errors.Is(err, sql.ErrNoRows) {
		return api.Session{}, ErrSessionNotFound
	}
	if err != nil {
		return api.Session{}, fmt.Errorf("get session: %w", err)
	}
	return x, nil
}

// ownedSession loads a session and enforces the ownerHost rule.
func (s *Store) ownedSession(ctx context.Context, ownerHost, id string) (api.Session, error) {
	x, err := s.getSession(ctx, id)
	if err != nil {
		return x, err
	}
	if ownerHost != "" && x.Host != ownerHost {
		return api.Session{}, ErrSessionOwner
	}
	return x, nil
}

// EndSession marks a session ended. Ending an already ended session is a no-op.
func (s *Store) EndSession(ctx context.Context, ownerHost string, r api.EndSessionRequest) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if _, err := s.ownedSession(ctx, ownerHost, r.ID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "UPDATE sessions SET ended_at=? WHERE id=? AND ended_at IS NULL", s.ms(), r.ID)
	if err != nil {
		return fmt.Errorf("end session: %w", err)
	}
	return nil
}

// ListSessions returns up to api.SessionListLimit sessions, most recently
// seen first; aliveOnly keeps live ones.
func (s *Store) ListSessions(ctx context.Context, aliveOnly bool) ([]api.Session, error) {
	q := "SELECT " + sessionCols + " FROM sessions"
	if aliveOnly {
		q += " WHERE ended_at IS NULL AND last_seen_at >= ?1"
	}
	rows, err := s.db.QueryContext(ctx, q+fmt.Sprintf(" ORDER BY last_seen_at DESC LIMIT %d", api.SessionListLimit), s.aliveCutoff())
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	out := []api.Session{}
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

const messageCols = "id, from_session, from_host, to_session, body, created_at, COALESCE(read_at,0)"

// pruneSessions deletes sessions ended or unseen for longer than retention,
// read messages older than retention, and messages addressed to deleted
// sessions. Called from Reap.
func (s *Store) pruneSessions(ctx context.Context, now int64) (sessions, messages int64, err error) {
	cut := now - s.cfg.retention().Milliseconds()
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE last_seen_at < ?1 OR (ended_at IS NOT NULL AND ended_at < ?1)`, cut)
	if err != nil {
		return 0, 0, fmt.Errorf("prune sessions: %w", err)
	}
	sessions, _ = res.RowsAffected()
	res, err = s.db.ExecContext(ctx, `DELETE FROM messages WHERE (read_at IS NOT NULL AND read_at < ?1)
	  OR to_session NOT IN (SELECT id FROM sessions)`, cut)
	if err != nil {
		return sessions, 0, fmt.Errorf("prune messages: %w", err)
	}
	messages, _ = res.RowsAffected()
	return sessions, messages, nil
}
