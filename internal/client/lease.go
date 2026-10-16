package client

import (
	"context"
	"net/http"

	"github.com/felixn678/agent-mesh/internal/api"
)

// Daemon (host token) calls: claim, load, heartbeat and outcomes.

// Claim asks for a task fitting req; ErrNoTask when none.
func (c *Client) Claim(ctx context.Context, req api.ClaimRequest) (api.ClaimedTask, error) {
	return c.postClaim(ctx, "/api/v1/claim", req)
}

// ReportLoad posts hardware, load and throttle reason.
func (c *Client) ReportLoad(ctx context.Context, l api.HostLoad) error {
	_, err := c.do(ctx, http.MethodPost, "/api/v1/hosts/load", l, nil)
	return err
}

// Heartbeat extends a lease; *LeaseError means stop.
func (c *Client) Heartbeat(ctx context.Context, id int64, req api.HeartbeatRequest) error {
	_, err := c.do(ctx, http.MethodPost, taskPath(id, "heartbeat"), req, nil)
	return err
}

func (c *Client) outcome(ctx context.Context, id int64, op string, req any) (alreadyApplied bool, err error) {
	var r api.OutcomeResponse
	_, err = c.do(ctx, http.MethodPost, taskPath(id, op), req, &r)
	return r.AlreadyApplied, err
}

// Complete reports success; idempotent for the same fence.
func (c *Client) Complete(ctx context.Context, id int64, req api.CompleteRequest) (bool, error) {
	return c.outcome(ctx, id, "complete", req)
}

// Fail reports a failed attempt; idempotent for the same fence.
func (c *Client) Fail(ctx context.Context, id int64, req api.FailRequest) (bool, error) {
	return c.outcome(ctx, id, "fail", req)
}

// Release hands a task back without consuming an attempt; idempotent.
func (c *Client) Release(ctx context.Context, id int64, req api.ReleaseRequest) (bool, error) {
	return c.outcome(ctx, id, "release", req)
}
