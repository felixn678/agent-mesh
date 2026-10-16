package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

// maxDoneRows caps the done table so the report stays small over MCP.
const maxDoneRows = 50

// publishNotes explains the publish notes that need a human.
var publishNotes = map[string]string{
	"leftover_commit": "agent left uncommitted changes; the daemon committed them — review before merging",
	"pr_failed":       "branch pushed but the PR was not opened",
}

func writeAttention(b *strings.Builder, now time.Time, g byState) {
	b.WriteString("\n## Needs attention\n\n")
	var items []string
	for _, t := range g[api.StateRunning] {
		if t.LeaseExpiresAt != 0 && now.After(ms(t.LeaseExpiresAt)) {
			items = append(items, fmt.Sprintf("#%d lease expired %s ago on %s", t.ID,
				dur(now.Sub(ms(t.LeaseExpiresAt))), cell(hostOf(t))))
		}
	}
	for _, t := range g[api.StateDone] {
		if why, ok := publishNotes[t.PublishNote]; ok {
			items = append(items, fmt.Sprintf("#%d %s on %s: %s (%s)", t.ID, t.PublishNote, cell(hostOf(t)), why, cell(t.Branch)))
		}
	}
	for _, t := range g[api.StateCancelled] {
		if t.PRURL != "" {
			items = append(items, fmt.Sprintf("#%d cancelled but has PR %s — close it if unwanted", t.ID, cell(t.PRURL)))
		}
	}
	stuck := g[api.StateStuck]
	if len(stuck) == 0 && len(items) == 0 {
		b.WriteString("Nothing needs attention.\n")
		return
	}
	if len(stuck) > 0 {
		fmt.Fprintf(b, "Stuck (%d):\n\n", len(stuck))
		rows := make([][]string, 0, len(stuck))
		var hints []string
		for _, t := range stuck {
			rows = append(rows, []string{fmt.Sprintf("#%d", t.ID), cell(t.Repo), cell(hostOf(t)),
				fmt.Sprintf("%d/%d", t.Attempts, t.MaxAttempts), cell(t.Reason), cell(t.SessionID), cell(t.Worktree)})
			if h := resumeHint(t); h != "" {
				hints = append(hints, fmt.Sprintf("- #%d: `%s`", t.ID, h))
			}
		}
		table(b, []string{"#", "repo", "host", "attempts", "reason", "session", "worktree"}, rows)
		if len(hints) > 0 {
			b.WriteString("\nResume:\n\n" + strings.Join(hints, "\n") + "\n")
		}
	}
	if len(items) > 0 {
		if len(stuck) > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- " + strings.Join(items, "\n- ") + "\n")
	}
}

func writeDone(b *strings.Builder, done []api.Task) {
	if len(done) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## Done (%d)\n\n", len(done))
	shown := done
	if len(shown) > maxDoneRows {
		shown = shown[:maxDoneRows]
	}
	rows := make([][]string, 0, len(shown))
	for _, t := range shown {
		rows = append(rows, []string{fmt.Sprintf("#%d", t.ID), cell(t.Repo), cell(hostOf(t)), cell(t.Branch),
			cell(t.PRURL), money(t.CostUSD), fmt.Sprint(t.Attempts), cell(t.PublishNote)})
	}
	table(b, []string{"#", "repo", "host", "branch", "PR", "cost", "attempts", "note"}, rows)
	if n := len(done) - len(shown); n > 0 {
		fmt.Fprintf(b, "\n…and %d more.\n", n)
	}
}

func writeRunning(b *strings.Builder, now time.Time, running []api.Task) {
	if len(running) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## Running (%d)\n\n", len(running))
	rows := make([][]string, 0, len(running))
	for _, t := range running {
		lease := "-"
		if t.LeaseExpiresAt != 0 {
			if exp := ms(t.LeaseExpiresAt); now.After(exp) {
				lease = "expired " + dur(now.Sub(exp)) + " ago"
			} else {
				lease = "expires in " + dur(exp.Sub(now))
			}
		}
		rows = append(rows, []string{fmt.Sprintf("#%d", t.ID), cell(hostOf(t)), lease})
	}
	table(b, []string{"#", "host", "lease"}, rows)
}

func writeQueued(b *strings.Builder, queued []api.Task) {
	if len(queued) == 0 {
		return
	}
	per := map[string]int{}
	for _, t := range queued {
		per[t.Repo]++
	}
	repos := make([]string, 0, len(per))
	for r := range per {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	parts := make([]string, 0, len(repos))
	for _, r := range repos {
		parts = append(parts, fmt.Sprintf("%s: %d", cell(r), per[r]))
	}
	fmt.Fprintf(b, "\n## Queued (%d)\n\n%s\n", len(queued), strings.Join(parts, ", "))
}

func writeHosts(b *strings.Builder, now time.Time, hosts []api.Host) {
	if len(hosts) == 0 {
		return
	}
	b.WriteString("\n## Hosts\n\n")
	for _, h := range hosts {
		parts := []string{cell(h.Summary())}
		if h.LastSeenAt != 0 {
			parts = append(parts, "seen "+dur(now.Sub(ms(h.LastSeenAt)))+" ago")
		} else {
			parts = append(parts, "never seen")
		}
		if h.CPUModel != "" {
			parts = append(parts, cell(h.CPUModel))
		}
		parts = append(parts, fmt.Sprintf("cpu %.0f%%", h.CPUPct), fmt.Sprintf("mem avail %.0f%%", h.MemAvailPct),
			fmt.Sprintf("worktrees %d", h.AgentWorktrees), fmt.Sprintf("running %d", h.Running),
			"claude "+cell(h.ClaudeVersion), "agent "+cell(h.AgentVersion))
		b.WriteString("- " + strings.Join(parts, " · ") + "\n")
	}
}
