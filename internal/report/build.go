// Package report renders the morning report.
package report

import (
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

// Input is everything Build needs; it is pure over this data.
type Input struct {
	Now, Since time.Time
	Tasks      []api.Task // from store.ListForReport
	Hosts      []api.Host // from store.ListHosts
}

// Build renders the report as markdown. Skeleton: replaced by the report owner.
func Build(in Input) string {
	return "# Morning report\n\n(report not implemented)\n"
}
