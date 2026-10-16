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
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
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
	baseURL string
	token   string
	http    *http.Client
}

// New builds a client; baseURL is e.g. http://127.0.0.1:8787.
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

// --- admin: tasks, hosts, report ---

// AddTask enqueues a task.
func (c *Client) AddTask(ctx context.Context, spec api.TaskSpec) (api.Task, error) {
	var t api.Task
	_, err := c.do(ctx, http.MethodPost, "/api/v1/tasks", spec, &t)
	return t, err
}

// GetTask fetches one task.
func (c *Client) GetTask(ctx context.Context, id int64) (api.Task, error) {
	var t api.Task
	_, err := c.do(ctx, http.MethodGet, "/api/v1/tasks/"+strconv.FormatInt(id, 10), nil, &t)
	return t, err
}

// ListTasks lists tasks, newest first (max 500). state "" and zero since mean any.
func (c *Client) ListTasks(ctx context.Context, state string, since time.Time) ([]api.Task, error) {
	q := url.Values{}
	if state != "" {
		q.Set("state", state)
	}
	if !since.IsZero() {
		q.Set("since", sinceParam(since))
	}
	var out []api.Task
	_, err := c.do(ctx, http.MethodGet, "/api/v1/tasks?"+q.Encode(), nil, &out)
	return out, err
}

// CancelTask cancels a queued, running or stuck task.
func (c *Client) CancelTask(ctx context.Context, id int64) (api.Task, error) {
	var t api.Task
	_, err := c.do(ctx, http.MethodPost, taskPath(id, "cancel"), nil, &t)
	return t, err
}

// RequeueTask returns a stuck or cancelled task to the queue.
func (c *Client) RequeueTask(ctx context.Context, id int64) (api.Task, error) {
	var t api.Task
	_, err := c.do(ctx, http.MethodPost, taskPath(id, "requeue"), nil, &t)
	return t, err
}

// Hosts lists known hosts.
func (c *Client) Hosts(ctx context.Context) ([]api.Host, error) {
	var out []api.Host
	_, err := c.do(ctx, http.MethodGet, "/api/v1/hosts", nil, &out)
	return out, err
}

// WhoAmI reports the kind of token this client presents (admin, host or session).
func (c *Client) WhoAmI(ctx context.Context) (api.WhoAmI, error) {
	var out api.WhoAmI
	_, err := c.do(ctx, http.MethodGet, "/api/v1/whoami", nil, &out)
	return out, err
}

// Report fetches the markdown morning report; zero since uses the hub default.
func (c *Client) Report(ctx context.Context, since time.Time) (string, error) {
	path := "/api/v1/report"
	if !since.IsZero() {
		path += "?since=" + url.QueryEscape(sinceParam(since))
	}
	var s string
	_, err := c.do(ctx, http.MethodGet, path, nil, &s)
	return s, err
}

// --- host: lease and load ---

// Claim asks for a task fitting req; ErrNoTask when none.
func (c *Client) Claim(ctx context.Context, req api.ClaimRequest) (api.ClaimedTask, error) {
	var t api.ClaimedTask
	status, err := c.do(ctx, http.MethodPost, "/api/v1/claim", req, &t)
	if err == nil && status == http.StatusNoContent {
		return api.ClaimedTask{}, ErrNoTask
	}
	return t, err
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

// --- host or admin: sessions and messages ---

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
	secs := int(wait / time.Second)
	if secs < 0 {
		secs = 0
	}
	if secs > 50 {
		secs = 50
	}
	q := url.Values{"session": {session}, "wait": {strconv.Itoa(secs) + "s"}}
	if markRead {
		q.Set("mark_read", "1")
	}
	var out []api.Message
	_, err := c.doWithTimeout(ctx, time.Duration(secs)*time.Second+10*time.Second, http.MethodGet,
		"/api/v1/messages/inbox?"+q.Encode(), nil, &out)
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
