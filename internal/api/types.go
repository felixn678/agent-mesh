// Package api holds the wire types shared by the hub, CLI, MCP tools and
// daemons. All timestamps are unix milliseconds from the hub clock.
package api

// Task states.
const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateDone      = "done"
	StateStuck     = "stuck"
	StateCancelled = "cancelled"
)

// Error codes carried in ErrorBody.Code.
const (
	CodeLeaseLost    = "lease_lost"   // fence or host moved on; worker must stop
	CodeLeaseReaped  = "lease_reaped" // reaper stuck it at the cap; holder may still Complete/Fail late
	CodeCancelled    = "cancelled"    // task cancelled; worker must stop
	CodeConflict     = "conflict"     // state does not allow the operation
	CodeEnded        = "ended"        // target session has ended
	CodeInboxFull    = "inbox_full"   // recipient has too many unread messages
	CodeTooManyWaits = "too_many_waits"
	CodeInvalid      = "invalid"
	CodeNotFound     = "not_found"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeTooLarge     = "too_large"
	CodeInternal     = "internal"
	CodeUnavailable  = "unavailable" // hub shutting down or request cancelled; retry
)

// ErrorBody is the JSON body of every non-2xx hub response.
type ErrorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// TaskSpec is what a human or MCP caller submits. Empty optional fields take
// the defaults from WithDefaults. Hosts map ToolProfile to a tool allow list;
// a spec can never carry tools itself.
type TaskSpec struct {
	Repo           string  `json:"repo"`
	BaseBranch     string  `json:"base_branch,omitempty"`
	Prompt         string  `json:"prompt"`
	PermissionMode string  `json:"permission_mode,omitempty"`
	ToolProfile    string  `json:"tool_profile,omitempty"`
	MaxBudgetUSD   float64 `json:"max_budget_usd"`
	Host           string  `json:"host,omitempty"` // optional constraint
	MaxAttempts    int     `json:"max_attempts,omitempty"`
	// TargetHost / TargetSession restrict who may claim the task. A task with
	// a TargetSession is claimable only by that interactive session.
	TargetHost    string `json:"target_host,omitempty"`
	TargetSession string `json:"target_session,omitempty"`
	// CreatedBySession is the session that asked for the work; the hub
	// messages it when the task completes, fails or gets stuck. The hub
	// verifies the session belongs to the caller's host.
	CreatedBySession string `json:"created_by_session,omitempty"`
}

// Task is a stored task. Nullable columns are zero values when unset.
type Task struct {
	ID               int64   `json:"id"`
	Repo             string  `json:"repo"`
	BaseBranch       string  `json:"base_branch"`
	Prompt           string  `json:"prompt"`
	PermissionMode   string  `json:"permission_mode"`
	ToolProfile      string  `json:"tool_profile"`
	MaxBudgetUSD     float64 `json:"max_budget_usd"`
	Host             string  `json:"host,omitempty"`
	State            string  `json:"state"`
	ClaimedBy        string  `json:"claimed_by,omitempty"`
	Fence            int64   `json:"fence"`
	Attempts         int     `json:"attempts"`
	MaxAttempts      int     `json:"max_attempts"`
	LeaseExpiresAt   int64   `json:"lease_expires_at,omitempty"`
	LastHeartbeat    int64   `json:"last_heartbeat_at,omitempty"`
	Branch           string  `json:"branch,omitempty"`
	PRURL            string  `json:"pr_url,omitempty"`
	SessionID        string  `json:"session_id,omitempty"`
	Worktree         string  `json:"worktree,omitempty"`
	CostUSD          float64 `json:"cost_usd"`
	PublishNote      string  `json:"publish_note,omitempty"`
	Reason           string  `json:"reason,omitempty"`
	CreatedBy        string  `json:"created_by,omitempty"`
	TargetHost       string  `json:"target_host,omitempty"`
	TargetSession    string  `json:"target_session,omitempty"`
	CreatedBySession string  `json:"created_by_session,omitempty"`
	Summary          string  `json:"summary,omitempty"`
	CreatedAt        int64   `json:"created_at"`
	UpdatedAt        int64   `json:"updated_at"`
	FinishedAt       int64   `json:"finished_at,omitempty"`
}
