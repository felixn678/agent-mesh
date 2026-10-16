package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

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
	res, err := s.db.ExecContext(ctx, `INSERT INTO sessions (id, host, cwd, name, repo, started_at, last_seen_at)
	  VALUES (?1,?2,?3,?4,?6,?5,?5)
	  ON CONFLICT(id) DO UPDATE SET cwd=excluded.cwd, name=excluded.name, last_seen_at=excluded.last_seen_at,
	    repo=CASE WHEN excluded.repo <> '' THEN excluded.repo ELSE sessions.repo END,
	    ended_at=NULL, started_at=CASE WHEN ended_at IS NULL THEN started_at ELSE excluded.started_at END
	  WHERE sessions.host = excluded.host`, r.ID, host, r.Cwd, r.Name, now, repo)
	if err != nil {
		return api.Session{}, fmt.Errorf("register session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return api.Session{}, ErrSessionOwner
	}
	return s.getSession(ctx, r.ID)
}

const sessionCols = `id, host, cwd, name, repo, started_at, last_seen_at, COALESCE(ended_at,0),
  (ended_at IS NULL AND last_seen_at >= ?1)`

func scanSession(r scanner) (api.Session, error) {
	var x api.Session
	err := r.Scan(&x.ID, &x.Host, &x.Cwd, &x.Name, &x.Repo, &x.StartedAt, &x.LastSeenAt, &x.EndedAt, &x.Alive)
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

// SendMessage stores a message from an existing session (owned by ownerHost
// unless "") to an existing session. The body is never interpreted.
func (s *Store) SendMessage(ctx context.Context, ownerHost string, r api.SendMessageRequest) (api.Message, error) {
	if err := r.Validate(); err != nil {
		return api.Message{}, err
	}
	from, err := s.ownedSession(ctx, ownerHost, r.FromSession)
	if err != nil {
		return api.Message{}, err
	}
	to, err := s.getSession(ctx, r.ToSession)
	if err != nil {
		return api.Message{}, err
	}
	if to.EndedAt != 0 {
		return api.Message{}, ErrSessionEnded
	}
	m := api.Message{FromSession: r.FromSession, FromHost: from.Host, ToSession: r.ToSession, Body: r.Body, CreatedAt: s.ms()}
	// The cap check and insert are one statement, so concurrent senders cannot overshoot it.
	err = s.db.QueryRowContext(ctx, `INSERT INTO messages (from_session, from_host, to_session, body, created_at)
	  SELECT ?1,?2,?3,?4,?5 WHERE (SELECT COUNT(*) FROM messages WHERE to_session=?3 AND read_at IS NULL) < ?6
	  RETURNING id`, m.FromSession, m.FromHost, m.ToSession, m.Body, m.CreatedAt, api.MaxUnreadPerSession).Scan(&m.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Message{}, ErrInboxFull
	}
	if err != nil {
		return api.Message{}, fmt.Errorf("insert message: %w", err)
	}
	s.notify.signal(m.ToSession)
	return m, nil
}

const messageCols = "id, from_session, from_host, to_session, body, created_at, COALESCE(read_at,0)"

// Inbox returns up to api.InboxLimit unread messages for session, oldest
// first. With markRead they are marked read atomically, so concurrent
// readers never receive the same message twice.
func (s *Store) Inbox(ctx context.Context, ownerHost, session string, markRead bool) ([]api.Message, error) {
	if !api.ValidSessionID(session) {
		return nil, &api.ValidationError{Msg: "invalid session id"}
	}
	sess, err := s.ownedSession(ctx, ownerHost, session)
	if err != nil {
		return nil, err
	}
	if sess.EndedAt != 0 {
		return nil, ErrSessionEnded
	}
	sel := fmt.Sprintf(`SELECT id FROM messages WHERE to_session=?1 AND read_at IS NULL ORDER BY id LIMIT %d`, api.InboxLimit)
	var rows *sql.Rows
	if markRead {
		rows, err = s.db.QueryContext(ctx, "UPDATE messages SET read_at=?2 WHERE id IN ("+sel+") RETURNING "+messageCols, session, s.ms())
	} else {
		rows, err = s.db.QueryContext(ctx, "SELECT "+messageCols+" FROM messages WHERE id IN ("+sel+")", session)
	}
	if err != nil {
		return nil, fmt.Errorf("inbox: %w", err)
	}
	defer rows.Close()
	out := []api.Message{}
	for rows.Next() {
		var m api.Message
		if err := rows.Scan(&m.ID, &m.FromSession, &m.FromHost, &m.ToSession, &m.Body, &m.CreatedAt, &m.ReadAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

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
