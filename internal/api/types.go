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
	CodeLeaseLost    = "lease_lost" // fence or host moved on; worker must stop
	CodeCancelled    = "cancelled"  // task cancelled; worker must stop
	CodeConflict     = "conflict"   // state does not allow the operation
	CodeEnded        = "ended"      // target session has ended
	CodeInboxFull    = "inbox_full" // recipient has too many unread messages
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

// HostLoad is what a daemon reports about itself on every poll tick.
type HostLoad struct {
	Cores           int     `json:"cores"`
	RAMGB           int     `json:"ram_gb"`
	CPUModel        string  `json:"cpu_model,omitempty"`
	Slots           int     `json:"slots"`
	SlotsOverridden bool    `json:"slots_overridden,omitempty"`
	CPUPct          float64 `json:"cpu_pct"`
	MemAvailPct     float64 `json:"mem_avail_pct"`
	AgentWorktrees  int     `json:"agent_worktrees"`
	Running         int     `json:"running"`
	ThrottleReason  string  `json:"throttle_reason,omitempty"`
	ClaudeVersion   string  `json:"claude_version,omitempty"`
	AgentVersion    string  `json:"agent_version,omitempty"`
}

// Host is a stored host row. The name always comes from the host token.
type Host struct {
	Name string `json:"name"`
	HostLoad
	LastSeenAt int64 `json:"last_seen_at"`
}

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

// ReleaseRequest hands a task back without consuming an attempt.
type ReleaseRequest struct {
	Fence  int64  `json:"fence"`
	Reason string `json:"reason,omitempty"`
}

// OutcomeResponse is the 200 body of complete/fail/release. AlreadyApplied is
// true when the same outcome was recorded earlier (idempotent replay).
type OutcomeResponse struct {
	AlreadyApplied bool `json:"already_applied,omitempty"`
}

// Session is a registered Claude Code session. Alive is computed by the hub:
// not ended and seen within SessionAliveWindow.
type Session struct {
	ID         string `json:"id"`
	Host       string `json:"host"`
	Cwd        string `json:"cwd,omitempty"`
	Name       string `json:"name,omitempty"`
	Repo       string `json:"repo,omitempty"` // normalized project key
	StartedAt  int64  `json:"started_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	EndedAt    int64  `json:"ended_at,omitempty"`
	Alive      bool   `json:"alive"`
}

// RegisterSessionRequest registers or refreshes (heartbeat) a session.
type RegisterSessionRequest struct {
	ID   string `json:"id"`
	Cwd  string `json:"cwd,omitempty"`
	Name string `json:"name,omitempty"`
	Repo string `json:"repo,omitempty"` // git remote URL or key; normalized by the hub; empty keeps the stored value
}

// EndSessionRequest marks a session ended.
type EndSessionRequest struct {
	ID string `json:"id"`
}

// SendMessageRequest sends an opaque body to one exact session id.
type SendMessageRequest struct {
	FromSession string `json:"from_session"`
	ToSession   string `json:"to_session"`
	Body        string `json:"body"`
}

// Message is a stored session message.
type Message struct {
	ID          int64  `json:"id"`
	FromSession string `json:"from_session"`
	FromHost    string `json:"from_host"`
	ToSession   string `json:"to_session"`
	Body        string `json:"body"`
	CreatedAt   int64  `json:"created_at"`
	ReadAt      int64  `json:"read_at,omitempty"`
}

// InboxResponse is the body of an inbox poll with claimable=1: unread
// messages plus how many queued tasks this session could claim right now.
type InboxResponse struct {
	Messages       []Message `json:"messages"`
	ClaimableCount int       `json:"claimable_count"`
}
