package api

import (
	"fmt"
	"strings"
)

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
