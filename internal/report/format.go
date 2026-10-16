package report

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/felixn678/agent-mesh/internal/api"
)

// maxCell bounds any free-text value (reasons, paths) placed in the report.
const maxCell = 200

// cell makes s safe for one markdown table cell: newlines become spaces,
// pipes are escaped, and long values are truncated to maxCell runes.
func cell(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "|", `\|`).Replace(s)
	if utf8.RuneCountInString(s) > maxCell {
		s = string([]rune(s)[:maxCell]) + "…"
	}
	if s == "" {
		return "-"
	}
	return s
}

// ms converts a hub unix-millisecond timestamp to time.
func ms(v int64) time.Time { return time.UnixMilli(v).UTC() }

// dur renders a duration coarsely for humans: "<1m", "12m", "3h05m", "2d4h".
func dur(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	d = d.Round(time.Minute)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	days := int(d.Hours()) / 24
	return fmt.Sprintf("%dd%dh", days, int(d.Hours())-days*24)
}

// money renders a USD amount.
func money(v float64) string { return fmt.Sprintf("$%.2f", v) }

// stamp renders a time for the header.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

// hostOf is the host a task ran on, falling back to its host constraint.
func hostOf(t api.Task) string {
	if t.ClaimedBy != "" {
		return t.ClaimedBy
	}
	return t.Host
}

// shellQuote single-quotes s unless it only contains plainly safe characters.
func shellQuote(s string) string {
	safe := true
	for _, r := range s {
		if !(r == '/' || r == '-' || r == '_' || r == '.' || r == '~' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			safe = false
			break
		}
	}
	if safe && s != "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// resumeHint is the command that reopens a stuck task's Claude session, or ""
// when the task lacks a worktree or session id, or either holds characters
// that could break the inline code span.
func resumeHint(t api.Task) string {
	if t.Worktree == "" || t.SessionID == "" || strings.ContainsAny(t.Worktree+t.SessionID, "`\r\n") {
		return ""
	}
	return "cd " + shellQuote(t.Worktree) + " && claude --resume " + shellQuote(t.SessionID)
}

// table writes a markdown table; rows must already be cell-escaped.
func table(b *strings.Builder, header []string, rows [][]string) {
	b.WriteString("| " + strings.Join(header, " | ") + " |\n")
	b.WriteString(strings.Repeat("|---", len(header)) + "|\n")
	for _, r := range rows {
		b.WriteString("| " + strings.Join(r, " | ") + " |\n")
	}
}
