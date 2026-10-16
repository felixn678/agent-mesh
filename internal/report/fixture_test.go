package report_test

import (
	"context"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/hubtest"
)

var bg = context.Background()

var caps = api.ClaimRequest{Repos: []string{"web", "api"}, Modes: []string{"acceptEdits"},
	Profiles: []string{"default"}, BudgetCap: 10}

// fixture drives a real hub through one overnight run: every task state and
// publish note the report treats specially, plus three hosts in different
// conditions. All placeholders; no real infrastructure names.
type fixture struct {
	t *testing.T
	h *hubtest.Hub
}

func (f fixture) add(repo string, maxAttempts int) api.Task {
	f.t.Helper()
	task, err := f.h.Admin().AddTask(bg, api.TaskSpec{Repo: repo, Prompt: "fix the thing",
		MaxBudgetUSD: 2, MaxAttempts: maxAttempts})
	if err != nil {
		f.t.Fatal(err)
	}
	return task
}

// claim adds a task and has host claim it at once, so ids map to claims.
func (f fixture) claim(host, repo string, maxAttempts int) (*client.Client, api.ClaimedTask) {
	f.t.Helper()
	f.add(repo, maxAttempts)
	c := f.h.Host(host)
	ct, err := c.Claim(bg, caps)
	if err != nil {
		f.t.Fatal(err)
	}
	return c, ct
}

func (f fixture) complete(host, repo string, r api.CompleteRequest) {
	f.t.Helper()
	c, ct := f.claim(host, repo, 0)
	r.Fence = ct.Fence
	if _, err := c.Complete(bg, ct.ID, r); err != nil {
		f.t.Fatal(err)
	}
}

func (f fixture) load(host string, l api.HostLoad) {
	f.t.Helper()
	if err := f.h.Host(host).ReportLoad(bg, l); err != nil {
		f.t.Fatal(err)
	}
}

func overnight(t *testing.T) *hubtest.Hub {
	f := fixture{t: t, h: hubtest.Start(t, "host-a", "host-b", "host-c")}
	clk := f.h.Clock // starts 08:00 UTC
	f.load("host-c", api.HostLoad{Cores: 4, RAMGB: 16, Slots: 1, CPUPct: 3, MemAvailPct: 80,
		ThrottleReason: "unhealthy: gh auth failed", ClaudeVersion: "2.1.0", AgentVersion: "0.1.0"})

	f.complete("host-a", "web", api.CompleteRequest{Branch: "agentmesh/1-fix-login",
		PRURL: "https://github.com/example/web/pull/12", SessionID: "sess-1", CostUSD: 0.42})
	clk.Advance(time.Minute)
	f.complete("host-a", "web", api.CompleteRequest{SessionID: "sess-2", CostUSD: 0.05, PublishNote: "no_changes"})
	clk.Advance(time.Minute)
	f.complete("host-b", "api", api.CompleteRequest{Branch: "agentmesh/3-bump-deps",
		PRURL: "https://github.com/example/api/pull/7", SessionID: "sess-3", CostUSD: 0.31, PublishNote: "leftover_commit"})
	clk.Advance(3 * time.Minute)

	f.claim("host-b", "api", 1) // #4: lease will expire at the attempt cap
	c, ct := f.claim("host-a", "web", 0)
	if _, err := c.Fail(bg, ct.ID, api.FailRequest{Fence: ct.Fence, CostUSD: 0.12,
		Reason:   "secret scan: possible token in config/app.env | line 3\npublish blocked",
		Worktree: "/srv/agent-mesh/worktrees/web-5"}); err != nil {
		t.Fatal(err)
	}
	clk.Advance(5 * time.Minute)
	f.claim("host-a", "api", 0) // #6: lease expires, below cap and not yet stale

	clk.Advance(10 * time.Minute)
	f.load("host-a", api.HostLoad{Cores: 8, RAMGB: 8, CPUModel: "Example CPU 8-core", Slots: 1,
		CPUPct: 85, MemAvailPct: 41, AgentWorktrees: 2, Running: 1, ThrottleReason: "cpu 85%",
		ClaudeVersion: "2.1.0", AgentVersion: "0.1.0"})
	clk.Advance(9 * time.Minute)
	f.load("host-b", api.HostLoad{Cores: 16, RAMGB: 64, Slots: 3, SlotsOverridden: true, CPUPct: 12,
		MemAvailPct: 70, ClaudeVersion: "2.0.9", AgentVersion: "0.1.0"})
	clk.Advance(time.Minute) // 08:30
	if _, err := f.h.Store.Reap(bg); err != nil {
		t.Fatal(err)
	}
	f.add("web", 0)
	f.add("web", 0)
	f.add("api", 0)
	return f.h
}
