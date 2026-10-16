package api

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Limits shared by REST, CLI and MCP callers.
const (
	MaxPromptBytes   = 64 * 1024
	MaxBudgetUSD     = 100.0
	MaxAttemptsLimit = 5
	maxTextLen       = 2000 // reason, publish_note, worktree, session id
	maxListLen       = 100  // entries in claim capability lists
	branchPrefix     = "agentmesh/"
)

// ValidationError marks bad caller input (HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// IsValidation reports whether err is (or wraps) a ValidationError.
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

var (
	repoRE    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	hostRE    = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	branchRE  = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,100}$`)
	prURLRE   = regexp.MustCompile(`^https://github\.com/[^/]+/[^/]+/pull/\d+$`)
	permModes = map[string]bool{"acceptEdits": true, "dontAsk": true, "plan": true}
)

// ValidRepo checks a logical repo key.
func ValidRepo(s string) bool { return repoRE.MatchString(s) }

// ValidHostName checks host names and tool profile names (same alphabet).
func ValidHostName(s string) bool { return hostRE.MatchString(s) }

// ValidPermissionMode allows only modes a task may request. bypassPermissions,
// auto and manual are deliberately absent.
func ValidPermissionMode(s string) bool { return permModes[s] }

// ValidBranch checks git ref syntax conservatively (safe as a CLI argument
// and as a path component): no leading '-', "..", "//", trailing '/', '.' or
// ".lock".
func ValidBranch(s string) bool {
	if !branchRE.MatchString(s) || strings.HasPrefix(s, "-") ||
		strings.Contains(s, "..") || strings.Contains(s, "//") {
		return false
	}
	return !strings.HasSuffix(s, "/") && !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, ".lock")
}

// ValidBaseBranch is ValidBranch plus a refusal of the agentmesh/ namespace,
// so a task can never base work on another agent's branch.
func ValidBaseBranch(s string) bool {
	return ValidBranch(s) && !strings.HasPrefix(s, branchPrefix)
}

// ValidPRURL accepts an empty URL or a github.com pull request URL.
func ValidPRURL(s string) bool { return s == "" || prURLRE.MatchString(s) }

// WithDefaults returns a copy with optional fields filled in.
func (s TaskSpec) WithDefaults() TaskSpec {
	if s.BaseBranch == "" {
		s.BaseBranch = "main"
	}
	if s.PermissionMode == "" {
		s.PermissionMode = "acceptEdits"
	}
	if s.ToolProfile == "" {
		s.ToolProfile = "default"
	}
	if s.MaxAttempts == 0 {
		s.MaxAttempts = 3
	}
	return s
}

// Validate checks a spec. Empty optional fields are treated as their
// defaults, so it is safe to call before or after WithDefaults.
func (s TaskSpec) Validate() error {
	s = s.WithDefaults()
	switch {
	case repoErr(s.Repo) != nil:
		return repoErr(s.Repo)
	case s.TargetHost != "" && !ValidHostName(s.TargetHost):
		return invalid("target_host must match %s", hostRE)
	case s.TargetSession != "" && !ValidSessionID(s.TargetSession):
		return invalid("target_session must match %s", sessionIDRE)
	case s.CreatedBySession != "" && !ValidSessionID(s.CreatedBySession):
		return invalid("created_by_session must match %s", sessionIDRE)
	case !ValidBaseBranch(s.BaseBranch):
		return invalid("base_branch is not a valid branch name (or uses the reserved %s prefix)", branchPrefix)
	case !ValidPermissionMode(s.PermissionMode):
		return invalid("permission_mode must be one of acceptEdits, dontAsk, plan")
	case !ValidHostName(s.ToolProfile):
		return invalid("tool_profile must match %s", hostRE)
	case s.Host != "" && !ValidHostName(s.Host):
		return invalid("host must match %s", hostRE)
	case !(s.MaxBudgetUSD > 0 && s.MaxBudgetUSD <= MaxBudgetUSD):
		return invalid("max_budget_usd must be > 0 and <= %v", MaxBudgetUSD)
	case strings.TrimSpace(s.Prompt) == "":
		return invalid("prompt must not be empty")
	case len(s.Prompt) > MaxPromptBytes || !utf8.ValidString(s.Prompt):
		return invalid("prompt must be valid UTF-8 of at most %d bytes", MaxPromptBytes)
	case s.MaxAttempts < 1 || s.MaxAttempts > MaxAttemptsLimit:
		return invalid("max_attempts must be 1..%d", MaxAttemptsLimit)
	}
	return nil
}

func repoErr(s string) error {
	_, err := CanonicalRepo(s)
	return err
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

func checkText(name string, s string) error {
	if len(s) > maxTextLen || !utf8.ValidString(s) {
		return invalid("%s must be valid UTF-8 of at most %d bytes", name, maxTextLen)
	}
	return nil
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

// Validate checks a release request.
func (r ReleaseRequest) Validate() error {
	if err := checkFence(r.Fence); err != nil {
		return err
	}
	return checkText("reason", r.Reason)
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

// Validate checks a load report; daemons are trusted but not to be careless.
func (l HostLoad) Validate() error {
	pct := func(v float64) bool { return v >= 0 && v <= 100 }
	switch {
	case l.Cores < 0 || l.RAMGB < 0 || l.Slots < 0 || l.AgentWorktrees < 0 || l.Running < 0:
		return invalid("load counters must be >= 0")
	case !pct(l.CPUPct) || !pct(l.MemAvailPct):
		return invalid("cpu_pct and mem_avail_pct must be within 0..100")
	}
	for name, v := range map[string]string{"cpu_model": l.CPUModel, "throttle_reason": l.ThrottleReason,
		"claude_version": l.ClaudeVersion, "agent_version": l.AgentVersion} {
		if len(v) > 100 || !utf8.ValidString(v) {
			return invalid("%s must be valid UTF-8 of at most 100 bytes", name)
		}
	}
	return nil
}
