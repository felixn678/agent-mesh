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

// anyKey is the broadcast key: every waiter subscribes to it as well as to
// its own session, so task-queue changes can wake idle sessions.
const anyKey = "*"

func (n *notifier) subscribe(keys ...string) (ch chan struct{}, cancel func()) {
	ch = make(chan struct{}, 1)
	n.mu.Lock()
	for _, k := range keys {
		if n.waiters[k] == nil {
			n.waiters[k] = map[chan struct{}]struct{}{}
		}
		n.waiters[k][ch] = struct{}{}
	}
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		for _, k := range keys {
			delete(n.waiters[k], ch)
			if len(n.waiters[k]) == 0 {
				delete(n.waiters, k)
			}
		}
		n.mu.Unlock()
	}
}

func (n *notifier) signalAll() { n.signal(anyKey) }

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
	r, err := s.waitInbox(ctx, ownerHost, session, markRead, wait, false)
	return r.Messages, err
}

// WaitInboxClaimable is WaitInbox that also returns, without claiming
// anything, as soon as a task this session could claim exists. ClaimableCount
// is filled on every return (including timeout).
func (s *Store) WaitInboxClaimable(ctx context.Context, ownerHost, session string, markRead bool, wait time.Duration) (api.InboxResponse, error) {
	return s.waitInbox(ctx, ownerHost, session, markRead, wait, true)
}

func (s *Store) waitInbox(ctx context.Context, ownerHost, session string, markRead bool, wait time.Duration, claimable bool) (api.InboxResponse, error) {
	if wait > MaxInboxWait {
		wait = MaxInboxWait
	}
	if !api.ValidSessionID(session) {
		return api.InboxResponse{}, &api.ValidationError{Msg: "invalid session id"}
	}
	ch, cancel := s.notify.subscribe(session, anyKey)
	defer cancel()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	safety := time.NewTicker(inboxPollSafety)
	defer safety.Stop()
	check := func() (api.InboxResponse, bool, error) {
		msgs, err := s.Inbox(ctx, ownerHost, session, markRead)
		r := api.InboxResponse{Messages: msgs}
		if err != nil {
			return r, true, err
		}
		if claimable {
			if r.ClaimableCount, err = s.ClaimableCount(ctx, session); err != nil {
				return r, true, err
			}
		}
		return r, len(msgs) > 0 || r.ClaimableCount > 0 || wait <= 0, nil
	}
	for {
		r, done, err := check()
		if err != nil || done {
			return r, err
		}
		select {
		case <-ch:
		case <-safety.C: // covers a missed signal
		case <-timer.C:
			r, _, err = check() // final check, then give up
			return r, err
		case <-ctx.Done():
			return api.InboxResponse{Messages: []api.Message{}}, ctx.Err()
		}
	}
}
