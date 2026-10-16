package mcptools

import (
	"context"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felixn678/agent-mesh/internal/mcptools/format"
	"github.com/felixn678/agent-mesh/internal/report"
)

// HostsIn controls the hosts tool.
type HostsIn struct {
	Verbose bool `json:"verbose,omitempty"`
}

// ReportIn sets the morning report window.
type ReportIn struct {
	SinceHours int `json:"since_hours,omitempty" jsonschema:"default 24"`
}

func (t tools) addHostReportTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{Name: "hosts", Description: "Hosts with hardware, slots and load."}, t.hosts)
	mcp.AddTool(srv, &mcp.Tool{Name: "morning_report", Description: "Overnight task report."}, t.morningReport)
}

func (t tools) hosts(ctx context.Context, _ *mcp.CallToolRequest, in HostsIn) (*mcp.CallToolResult, any, error) {
	list, err := t.st.ListHosts(ctx)
	if err != nil {
		return storeErr(err), nil, nil
	}
	if len(list) == 0 {
		return textResult("no hosts"), nil, nil
	}
	now := t.st.Now()
	lines := make([]string, len(list))
	for i, h := range list {
		lines[i] = format.HostLine(h, now, in.Verbose)
	}
	return textResult(strings.Join(lines, "\n")), nil, nil
}

func (t tools) morningReport(ctx context.Context, _ *mcp.CallToolRequest, in ReportIn) (*mcp.CallToolResult, any, error) {
	hours := in.SinceHours
	if hours <= 0 {
		hours = 24
	}
	now := t.st.Now()
	since := now.Add(-time.Duration(hours) * time.Hour)
	tasks, err := t.st.ListForReport(ctx, since)
	if err != nil {
		return storeErr(err), nil, nil
	}
	hosts, err := t.st.ListHosts(ctx)
	if err != nil {
		return storeErr(err), nil, nil
	}
	return textResult(report.Build(report.Input{Now: now, Since: since, Tasks: tasks, Hosts: hosts})), nil, nil
}
