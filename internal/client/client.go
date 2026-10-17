// Package client is the typed REST client for the hub, used by the CLI and
// daemons. Network errors are returned raw; hub errors are typed.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/version"
)

// CallTimeout bounds each request.
const CallTimeout = 30 * time.Second

// maxResponseBytes bounds what the client will read from the hub.
const maxResponseBytes = 8 << 20

// ErrNoTask is returned by Claim when the queue has nothing eligible (204).
var ErrNoTask = errors.New("no task available")

// LeaseError is a 409 lease_lost / cancelled response: the worker must stop.
type LeaseError struct{ Code, Message string }

func (e *LeaseError) Error() string { return e.Code + ": " + e.Message }

// IsLeaseLost reports whether err is a lease_lost, lease_reaped or
// cancelled LeaseError: in every case the heartbeat was refused.
func IsLeaseLost(err error) bool {
	var le *LeaseError
	return errors.As(err, &le)
}

// IsLeaseReaped reports a lease_reaped LeaseError: the reaper marked the task
// stuck at its attempt cap, but this holder may still finish it and report
// Complete or Fail (the hub accepts that late outcome).
func IsLeaseReaped(err error) bool {
	var le *LeaseError
	return errors.As(err, &le) && le.Code == api.CodeLeaseReaped
}

// HTTPError is any other non-2xx hub response.
type HTTPError struct {
	Status        int
	Code, Message string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("hub returned %d %s: %s", e.Status, e.Code, e.Message)
}

// Client talks to one hub with one token.
type Client struct {
	baseURL    string
	token      string
	http       *http.Client
	hubVersion atomic.Value // string from the last response's version header
}

// New builds a client; baseURL is e.g. http://127.0.0.1:47878.
func New(baseURL, token string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: &http.Client{}}
}

// do sends one request with a per-call timeout. in/out may be nil. It returns
// the status so callers can special-case 204.
func (c *Client) do(ctx context.Context, method, path string, in, out any) (int, error) {
	return c.doWithTimeout(ctx, CallTimeout, method, path, in, out)
}

func (c *Client) doWithTimeout(ctx context.Context, timeout time.Duration, method, path string, in, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	c.hubVersion.Store(resp.Header.Get(version.Header))
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode >= 300 {
		return resp.StatusCode, decodeError(resp.StatusCode, data)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if s, ok := out.(*string); ok {
			*s = string(data)
		} else if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// HubVersion is the hub's version as reported on the last response; "" before
// any response and from hubs too old to send it.
func (c *Client) HubVersion() string {
	v, _ := c.hubVersion.Load().(string)
	return v
}

func decodeError(status int, data []byte) error {
	var eb api.ErrorBody
	if json.Unmarshal(data, &eb) != nil || eb.Code == "" {
		return &HTTPError{Status: status, Code: "unknown", Message: strings.TrimSpace(string(data))}
	}
	if status == http.StatusConflict && (eb.Code == api.CodeLeaseLost || eb.Code == api.CodeLeaseReaped || eb.Code == api.CodeCancelled) {
		return &LeaseError{Code: eb.Code, Message: eb.Error}
	}
	return &HTTPError{Status: status, Code: eb.Code, Message: eb.Error}
}

func taskPath(id int64, op string) string {
	return "/api/v1/tasks/" + strconv.FormatInt(id, 10) + "/" + op
}

func sinceParam(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// postClaim posts a claim request to path and maps 204 to ErrNoTask (shared
// by daemon and session claims).
func (c *Client) postClaim(ctx context.Context, path string, req any) (api.ClaimedTask, error) {
	var t api.ClaimedTask
	status, err := c.do(ctx, http.MethodPost, path, req, &t)
	if err == nil && status == http.StatusNoContent {
		return api.ClaimedTask{}, ErrNoTask
	}
	return t, err
}

// maxInboxWait is the longest long poll the hub holds an inbox request.
const maxInboxWait = 50 * time.Second

// inboxWaitQuery clamps wait to [0, maxInboxWait] and returns the query and
// the matching client timeout (the wait plus slack for the response).
func inboxWaitQuery(session string, markRead bool, wait time.Duration) (url.Values, time.Duration) {
	secs := min(max(int(wait/time.Second), 0), int(maxInboxWait/time.Second))
	q := url.Values{"session": {session}, "wait": {strconv.Itoa(secs) + "s"}}
	if markRead {
		q.Set("mark_read", "1")
	}
	return q, time.Duration(secs)*time.Second + 10*time.Second
}
