// Package report renders the morning report.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

// Input is everything Build needs; it is pure over this data.
type Input struct {
	Now, Since time.Time
	Tasks      []api.Task // from store.ListForReport
	Hosts      []api.Host // from store.ListHosts
}

// byState groups tasks by state, each group ordered by id ascending.
type byState map[string][]api.Task

func group(tasks []api.Task) byState {
	g := byState{}
	for _, t := range tasks {
		g[t.State] = append(g[t.State], t)
	}
	for _, ts := range g {
		sort.Slice(ts, func(i, j int) bool { return ts[i].ID < ts[j].ID })
	}
	return g
}

// Build renders the report as markdown. It is deterministic: all times come
// from in.Now and the rows themselves.
func Build(in Input) string {
	var b strings.Builder
	g := group(in.Tasks)
	b.WriteString("# Morning report\n\n")
	fmt.Fprintf(&b, "Window: %s → %s (%s)\n\n", stamp(in.Since), stamp(in.Now), dur(in.Now.Sub(in.Since)))
	if len(in.Tasks) == 0 {
		b.WriteString("Nothing ran in this window.\n")
	} else {
		writeTotals(&b, in.Tasks, g)
		writeAttention(&b, in.Now, g)
		writeDone(&b, g[api.StateDone])
		writeRunning(&b, in.Now, g[api.StateRunning])
		writeQueued(&b, g[api.StateQueued])
	}
	writeHosts(&b, in.Now, in.Hosts)
	return b.String()
}

func writeTotals(b *strings.Builder, tasks []api.Task, g byState) {
	var cost float64
	for _, t := range tasks {
		cost += t.CostUSD
	}
	var parts []string
	for _, s := range []string{api.StateDone, api.StateStuck, api.StateRunning, api.StateQueued, api.StateCancelled} {
		parts = append(parts, fmt.Sprintf("%d %s", len(g[s]), s))
	}
	fmt.Fprintf(b, "Totals: %s. Cost: %s (client-side estimate, not a bill).\n", strings.Join(parts, ", "), money(cost))
}
