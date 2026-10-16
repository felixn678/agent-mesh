package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/felixn678/agent-mesh/internal/api"
)

// Session messages: send, inbox reads and acknowledgement.

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

// AckMessages marks the listed unread messages of session read. The session
// must belong to ownerHost (session tokens only ack their own host's inboxes).
// Ids that are unknown, already read or addressed elsewhere are ignored, so a
// retried ack is harmless. Returns how many messages changed.
func (s *Store) AckMessages(ctx context.Context, ownerHost string, r api.AckMessagesRequest) (int64, error) {
	if err := r.Validate(); err != nil {
		return 0, err
	}
	if _, err := s.ownedSession(ctx, ownerHost, r.Session); err != nil {
		return 0, err
	}
	ids, _ := json.Marshal(r.IDs) // []int64 cannot fail to marshal
	res, err := s.db.ExecContext(ctx, `UPDATE messages SET read_at=?1
	  WHERE to_session=?2 AND read_at IS NULL AND id IN (SELECT value FROM json_each(?3))`,
		s.ms(), r.Session, string(ids))
	if err != nil {
		return 0, fmt.Errorf("ack messages: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
