package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

// Interactive-session workers use the session token. Every call names the
// session; the hub only accepts sessions of the token's host.

func sessionTaskPath(session string, id int64, op string) string {
	return "/api/v1/sessions/" + url.PathEscape(session) + "/tasks/" + strconv.FormatInt(id, 10) + "/" + op
}

// SessionClaim claims the oldest queued task for this session's project that
// targets this host/session (or nobody); ErrNoTask when none. Fence must be
// echoed on every later call.
func (c *Client) SessionClaim(ctx context.Context, session string) (api.ClaimedTask, error) {
	return c.SessionClaimWith(ctx, session, api.SessionClaimRequest{})
}

// SessionHeartbeat extends the session's lease (30 min by default); a
// *LeaseError means stop working on the task.
func (c *Client) SessionHeartbeat(ctx context.Context, session string, id int64, req api.HeartbeatRequest) error {
	_, err := c.do(ctx, http.MethodPost, sessionTaskPath(session, id, "heartbeat"), req, nil)
	return err
}

func (c *Client) sessionOutcome(ctx context.Context, session string, id int64, op string, req any) (bool, error) {
	var r api.OutcomeResponse
	_, err := c.do(ctx, http.MethodPost, sessionTaskPath(session, id, op), req, &r)
	return r.AlreadyApplied, err
}

// SessionComplete reports success of a session-held task; idempotent.
func (c *Client) SessionComplete(ctx context.Context, session string, id int64, req api.CompleteRequest) (bool, error) {
	return c.sessionOutcome(ctx, session, id, "complete", req)
}

// SessionFail reports a failed attempt of a session-held task; idempotent.
func (c *Client) SessionFail(ctx context.Context, session string, id int64, req api.FailRequest) (bool, error) {
	return c.sessionOutcome(ctx, session, id, "fail", req)
}

// SessionRelease hands a session-held task back without using an attempt.
func (c *Client) SessionRelease(ctx context.Context, session string, id int64, req api.ReleaseRequest) (bool, error) {
	return c.sessionOutcome(ctx, session, id, "release", req)
}

// InboxWaitClaimable is InboxWait that also returns early, without claiming,
// when a task this session could claim exists. ClaimableCount is set on every
// return. An empty Messages and zero ClaimableCount means the wait timed out.
func (c *Client) InboxWaitClaimable(ctx context.Context, session string, markRead bool, wait time.Duration) (api.InboxResponse, error) {
	secs := min(max(int(wait/time.Second), 0), 50)
	q := url.Values{"session": {session}, "wait": {strconv.Itoa(secs) + "s"}, "claimable": {"1"}}
	if markRead {
		q.Set("mark_read", "1")
	}
	var out api.InboxResponse
	_, err := c.doWithTimeout(ctx, time.Duration(secs)*time.Second+10*time.Second, http.MethodGet,
		"/api/v1/messages/inbox?"+q.Encode(), nil, &out)
	return out, err
}

// SessionClaimWith is SessionClaim with a request body. Reuse the same
// RequestID when retrying a claim whose response was lost: the hub then
// returns the task this session already holds (same fence) instead of
// claiming another one.
func (c *Client) SessionClaimWith(ctx context.Context, session string, req api.SessionClaimRequest) (api.ClaimedTask, error) {
	var t api.ClaimedTask
	status, err := c.do(ctx, http.MethodPost, "/api/v1/sessions/"+url.PathEscape(session)+"/claim", req, &t)
	if err == nil && status == http.StatusNoContent {
		return api.ClaimedTask{}, ErrNoTask
	}
	return t, err
}
