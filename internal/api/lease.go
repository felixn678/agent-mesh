package api

import (
	"math"
	"regexp"
)

// Lease wire types: what a worker (daemon or interactive session) sends and
// receives while holding a task, with their validation.

// ClaimRequest carries the caller's capabilities; the hub only hands out
// tasks that fit them. The host name comes from the token, not the body.
type ClaimRequest struct {
	Repos     []string  `json:"repos"`
	Modes     []string  `json:"modes"`
	Profiles  []string  `json:"profiles"`
	BudgetCap float64   `json:"budget_cap"`
	Load      *HostLoad `json:"load,omitempty"`
	// RequestID (optional, daemon-generated per claim attempt) makes a
	// retried claim idempotent: the same host and RequestID get the task they
	// already hold back with the same fence instead of a second task.
	RequestID string `json:"request_id,omitempty"`
}

// Validate checks claim capabilities. Empty lists are legal (claim nothing).
func (r ClaimRequest) Validate() error {
	if len(r.Repos) > maxListLen || len(r.Modes) > maxListLen || len(r.Profiles) > maxListLen {
		return invalid("capability lists are limited to %d entries", maxListLen)
	}
	if math.IsNaN(r.BudgetCap) || r.BudgetCap < 0 {
		return invalid("budget_cap out of range")
	}
	if err := checkRequestID(r.RequestID); err != nil {
		return err
	}
	if r.Load != nil {
		return r.Load.Validate()
	}
	return nil
}

// ClaimedTask is the subset of a task a worker needs; Fence must be echoed
// on every later call.
type ClaimedTask struct {
	ID             int64   `json:"id"`
	Fence          int64   `json:"fence"`
	Repo           string  `json:"repo"`
	BaseBranch     string  `json:"base_branch"`
	Prompt         string  `json:"prompt"`
	PermissionMode string  `json:"permission_mode"`
	ToolProfile    string  `json:"tool_profile"`
	MaxBudgetUSD   float64 `json:"max_budget_usd"`
}

// HeartbeatRequest extends a lease.
type HeartbeatRequest struct {
	Fence int64     `json:"fence"`
	Load  *HostLoad `json:"load,omitempty"`
}

// Validate checks a heartbeat request.
func (r HeartbeatRequest) Validate() error {
	if err := checkFence(r.Fence); err != nil {
		return err
	}
	if r.Load != nil {
		return r.Load.Validate()
	}
	return nil
}

// CompleteRequest finishes a task successfully.
type CompleteRequest struct {
	Fence       int64   `json:"fence"`
	Branch      string  `json:"branch,omitempty"`
	PRURL       string  `json:"pr_url,omitempty"`
	SessionID   string  `json:"session_id,omitempty"`
	CostUSD     float64 `json:"cost_usd"`
	PublishNote string  `json:"publish_note,omitempty"`
	Summary     string  `json:"summary,omitempty"` // result text relayed to the creator session
}

// Validate checks a completion report.
func (r CompleteRequest) Validate() error {
	if err := checkFence(r.Fence); err != nil {
		return err
	}
	if err := checkCost(r.CostUSD); err != nil {
		return err
	}
	if r.Branch != "" && !ValidBranch(r.Branch) {
		return invalid("branch is not a valid branch name")
	}
	if !ValidPRURL(r.PRURL) {
		return invalid("pr_url must be an https://github.com/<owner>/<repo>/pull/<n> URL")
	}
	for name, v := range map[string]string{"session_id": r.SessionID, "publish_note": r.PublishNote, "summary": r.Summary} {
		if err := checkText(name, v); err != nil {
			return err
		}
	}
	return nil
}

// FailRequest ends an attempt unsuccessfully; Retry asks for requeue while
// attempts remain.
type FailRequest struct {
	Fence     int64   `json:"fence"`
	Retry     bool    `json:"retry"`
	CostUSD   float64 `json:"cost_usd"`
	Reason    string  `json:"reason,omitempty"`
	Worktree  string  `json:"worktree,omitempty"`
	SessionID string  `json:"session_id,omitempty"` // Claude session id of the worker run
}

// Validate checks a failure report.
func (r FailRequest) Validate() error {
	if err := checkFence(r.Fence); err != nil {
		return err
	}
	if err := checkCost(r.CostUSD); err != nil {
		return err
	}
	if err := checkText("reason", r.Reason); err != nil {
		return err
	}
	if err := checkText("session_id", r.SessionID); err != nil {
		return err
	}
	return checkText("worktree", r.Worktree)
}

// ReleaseRequest hands a task back without consuming an attempt.
type ReleaseRequest struct {
	Fence  int64  `json:"fence"`
	Reason string `json:"reason,omitempty"`
}

// Validate checks a release request.
func (r ReleaseRequest) Validate() error {
	if err := checkFence(r.Fence); err != nil {
		return err
	}
	return checkText("reason", r.Reason)
}

// OutcomeResponse is the 200 body of complete/fail/release. AlreadyApplied is
// true when the same outcome was recorded earlier (idempotent replay).
type OutcomeResponse struct {
	AlreadyApplied bool `json:"already_applied,omitempty"`
}

var requestIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// checkRequestID allows an empty id (no idempotency) or a short token.
func checkRequestID(id string) error {
	if id != "" && !requestIDRE.MatchString(id) {
		return invalid("request_id must match %s", requestIDRE)
	}
	return nil
}

func checkFence(f int64) error {
	if f < 1 {
		return invalid("fence must be >= 1")
	}
	return nil
}

func checkCost(c float64) error {
	if math.IsNaN(c) || math.IsInf(c, 0) || c < 0 || c > 1e6 {
		return invalid("cost_usd out of range")
	}
	return nil
}
