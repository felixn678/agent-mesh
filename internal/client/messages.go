package client

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

// Session registry and messages (session token).

// RegisterSession registers or refreshes (heartbeat) a Claude session.
func (c *Client) RegisterSession(ctx context.Context, req api.RegisterSessionRequest) (api.Session, error) {
	var s api.Session
	_, err := c.do(ctx, http.MethodPost, "/api/v1/sessions", req, &s)
	return s, err
}

// EndSession marks a session ended.
func (c *Client) EndSession(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodPost, "/api/v1/sessions/"+url.PathEscape(id)+"/end", nil, nil)
	return err
}

// ListSessions lists sessions (max 500); aliveOnly filters to live ones.
func (c *Client) ListSessions(ctx context.Context, aliveOnly bool) ([]api.Session, error) {
	path := "/api/v1/sessions"
	if aliveOnly {
		path += "?alive=1"
	}
	var out []api.Session
	_, err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// SendMessage sends an opaque body to one exact session id.
func (c *Client) SendMessage(ctx context.Context, req api.SendMessageRequest) (api.Message, error) {
	var m api.Message
	_, err := c.do(ctx, http.MethodPost, "/api/v1/messages", req, &m)
	return m, err
}

// Inbox returns up to 50 unread messages for session, oldest first. markRead
// consumes them on the hub before the response arrives (at most once: a lost
// response loses them). Prefer markRead=false followed by AckMessages with
// the ids actually delivered (at least once).
func (c *Client) Inbox(ctx context.Context, session string, markRead bool) ([]api.Message, error) {
	q := url.Values{"session": {session}}
	if markRead {
		q.Set("mark_read", "1")
	}
	var out []api.Message
	_, err := c.do(ctx, http.MethodGet, "/api/v1/messages/inbox?"+q.Encode(), nil, &out)
	return out, err
}

// InboxWait is Inbox as a long poll: with nothing unread the hub holds the
// request up to wait (max 50 s) and answers as soon as a message arrives. ctx
// cancellation aborts the wait. An empty slice means the wait timed out.
func (c *Client) InboxWait(ctx context.Context, session string, markRead bool, wait time.Duration) ([]api.Message, error) {
	q, timeout := inboxWaitQuery(session, markRead, wait)
	var out []api.Message
	_, err := c.doWithTimeout(ctx, timeout, http.MethodGet, "/api/v1/messages/inbox?"+q.Encode(), nil, &out)
	return out, err
}

// AckMessages marks delivered messages of session read; ids already read or
// not addressed to session are ignored, so retries are safe. Returns how many
// changed.
func (c *Client) AckMessages(ctx context.Context, session string, ids []int64) (int64, error) {
	var r api.AckMessagesResponse
	_, err := c.do(ctx, http.MethodPost, "/api/v1/messages/ack", api.AckMessagesRequest{Session: session, IDs: ids}, &r)
	return r.Acked, err
}
