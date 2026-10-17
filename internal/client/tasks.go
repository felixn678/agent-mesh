package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

// Admin and read calls: tasks, hosts, identity and the report.

// AddTask enqueues a task.
// Set spec.RequestID (and reuse it on retry) to make the add idempotent: a
// retry after a lost response returns the task the first attempt created.
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

// HostProfiles lists every machine's role note, detected tools and live sessions.
func (c *Client) HostProfiles(ctx context.Context) ([]api.HostProfile, error) {
	var out []api.HostProfile
	_, err := c.do(ctx, http.MethodGet, "/api/v1/hosts/profiles", nil, &out)
	return out, err
}

// SetHostNote sets (empty clears) the role note of machine name.
func (c *Client) SetHostNote(ctx context.Context, name, note string) error {
	_, err := c.do(ctx, http.MethodPut, "/api/v1/hosts/"+url.PathEscape(name)+"/note", api.HostNoteRequest{Note: note}, nil)
	return err
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
