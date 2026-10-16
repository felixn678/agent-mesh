package api

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

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

// Host is a stored host row. The name always comes from the host token.
type Host struct {
	Name string `json:"name"`
	HostLoad
	LastSeenAt int64 `json:"last_seen_at"`
}

// Summary is the one-line host description shared by the CLI, MCP tools and
// the morning report, e.g. "box: 8c/8GB → 1 slot, throttled: cpu 85%".
func (h Host) Summary() string {
	if h.Cores == 0 && h.RAMGB == 0 {
		return h.Name + ": hardware unknown"
	}
	slots := "slots"
	if h.Slots == 1 {
		slots = "slot"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %dc/%dGB → %d %s", h.Name, h.Cores, h.RAMGB, h.Slots, slots)
	if h.SlotsOverridden {
		b.WriteString(" (override)")
	}
	if h.ThrottleReason != "" {
		b.WriteString(", throttled: " + h.ThrottleReason)
	}
	return b.String()
}
