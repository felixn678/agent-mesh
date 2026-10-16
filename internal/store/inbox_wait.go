package store

import (
	"context"
	"sync"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

// MaxInboxWait caps long-poll holds.
const MaxInboxWait = 50 * time.Second

// inboxPollSafety re-queries a waiting inbox even without a signal.
const inboxPollSafety = 2 * time.Second

// notifier wakes inbox waiters in-process when a message arrives. It holds no
// database resources, so waiting never occupies the SQLite connection.
type notifier struct {
	mu      sync.Mutex
	waiters map[string]map[chan struct{}]struct{}
}

func newNotifier() *notifier { return &notifier{waiters: map[string]map[chan struct{}]struct{}{}} }

func (n *notifier) subscribe(session string) (ch chan struct{}, cancel func()) {
	ch = make(chan struct{}, 1)
	n.mu.Lock()
	if n.waiters[session] == nil {
		n.waiters[session] = map[chan struct{}]struct{}{}
	}
	n.waiters[session][ch] = struct{}{}
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		delete(n.waiters[session], ch)
		if len(n.waiters[session]) == 0 {
			delete(n.waiters, session)
		}
		n.mu.Unlock()
	}
}

func (n *notifier) signal(session string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for ch := range n.waiters[session] {
		select {
		case ch <- struct{}{}:
		default: // already signalled
		}
	}
}

// WaitInbox is Inbox that, when nothing is unread, holds up to wait (capped at
// MaxInboxWait) and returns as soon as a message is sent to session. It
// subscribes before checking, so a message arriving in between is not missed.
// An empty slice is returned on timeout.
func (s *Store) WaitInbox(ctx context.Context, ownerHost, session string, markRead bool, wait time.Duration) ([]api.Message, error) {
	if wait > MaxInboxWait {
		wait = MaxInboxWait
	}
	if !api.ValidSessionID(session) {
		return nil, &api.ValidationError{Msg: "invalid session id"}
	}
	ch, cancel := s.notify.subscribe(session)
	defer cancel()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	safety := time.NewTicker(inboxPollSafety)
	defer safety.Stop()
	for {
		msgs, err := s.Inbox(ctx, ownerHost, session, markRead)
		if err != nil || len(msgs) > 0 || wait <= 0 {
			return msgs, err
		}
		select {
		case <-ch:
		case <-safety.C: // covers a missed signal
		case <-timer.C:
			return s.Inbox(ctx, ownerHost, session, markRead) // final check, then give up
		case <-ctx.Done():
			return []api.Message{}, ctx.Err()
		}
	}
}
