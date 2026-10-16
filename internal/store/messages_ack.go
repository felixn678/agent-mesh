package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/felixn678/agent-mesh/internal/api"
)

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
