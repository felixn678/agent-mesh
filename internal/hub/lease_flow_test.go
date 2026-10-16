package hub_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/hubtest"
)

var bg = context.Background()

func caps() api.ClaimRequest {
	return api.ClaimRequest{Repos: []string{"r"}, Modes: []string{"acceptEdits"}, Profiles: []string{"default"}, BudgetCap: 10}
}

func addTask(t *testing.T, h *hubtest.Hub) api.Task {
	t.Helper()
	task, err := h.Admin().AddTask(bg, api.TaskSpec{Repo: "r", Prompt: "p", MaxBudgetUSD: 2})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestClaimHeartbeatCompleteViaClient(t *testing.T) {
	h := hubtest.Start(t)
	admin, host := h.Admin(), h.Host("host-a")
	if _, err := host.Claim(bg, caps()); !errors.Is(err, client.ErrNoTask) {
		t.Fatalf("empty queue: %v", err)
	}
	task := addTask(t, h)
	c, err := host.Claim(bg, caps())
	if err != nil || c.ID != task.ID || c.Fence != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	got, _ := admin.GetTask(bg, task.ID)
	if got.ClaimedBy != "host-a" {
		t.Fatalf("host not derived from token: %q", got.ClaimedBy)
	}
	if err := host.Heartbeat(bg, c.ID, api.HeartbeatRequest{Fence: c.Fence}); err != nil {
		t.Fatal(err)
	}
	if err := h.Host("host-b").Heartbeat(bg, c.ID, api.HeartbeatRequest{Fence: c.Fence}); !isCode(err, api.CodeLeaseLost) {
		t.Fatalf("other host heartbeat: %v", err)
	}
	req := api.CompleteRequest{Fence: c.Fence, Branch: "agentmesh/task-1-f1", PRURL: "https://github.com/o/r/pull/1", CostUSD: 0.4}
	if again, err := host.Complete(bg, c.ID, req); err != nil || again {
		t.Fatalf("%v %v", again, err)
	}
	if again, err := host.Complete(bg, c.ID, req); err != nil || !again {
		t.Fatalf("replay: %v %v", again, err)
	}
	if got, _ = admin.GetTask(bg, task.ID); got.State != api.StateDone || got.PRURL != req.PRURL {
		t.Fatalf("%+v", got)
	}
}

func isCode(err error, code string) bool {
	var le *client.LeaseError
	return errors.As(err, &le) && le.Code == code
}

func TestCancelledAndLeaseLostCodes(t *testing.T) {
	h := hubtest.Start(t)
	task := addTask(t, h)
	host := h.Host("host-a")
	c, _ := host.Claim(bg, caps())
	if _, err := h.Admin().CancelTask(bg, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := host.Heartbeat(bg, c.ID, api.HeartbeatRequest{Fence: c.Fence}); !isCode(err, api.CodeCancelled) {
		t.Fatalf("want cancelled: %v", err)
	}
	if _, err := host.Fail(bg, c.ID, api.FailRequest{Fence: c.Fence + 5}); !isCode(err, api.CodeLeaseLost) {
		t.Fatalf("want lease_lost: %v", err)
	}
	if !client.IsLeaseLost(&client.LeaseError{Code: "x"}) {
		t.Fatal("IsLeaseLost")
	}
}

func TestReleaseAndFailViaClient(t *testing.T) {
	h := hubtest.Start(t)
	addTask(t, h)
	host := h.Host("host-a")
	c, _ := host.Claim(bg, caps())
	if _, err := host.Release(bg, c.ID, api.ReleaseRequest{Fence: c.Fence, Reason: "no clone"}); err != nil {
		t.Fatal(err)
	}
	c2, err := h.Host("host-b").Claim(bg, caps())
	if err != nil || c2.Fence != c.Fence+1 {
		t.Fatalf("%+v %v", c2, err)
	}
	if _, err := h.Host("host-b").Fail(bg, c2.ID, api.FailRequest{Fence: c2.Fence, Retry: true, Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	got, _ := h.Admin().GetTask(bg, c.ID)
	if got.State != api.StateQueued || got.Attempts != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestHostLoadAndHostsList(t *testing.T) {
	h := hubtest.Start(t)
	load := api.HostLoad{Cores: 8, RAMGB: 8, Slots: 1, CPUPct: 85, ThrottleReason: "cpu 85%"}
	if err := h.Host("host-a").ReportLoad(bg, load); err != nil {
		t.Fatal(err)
	}
	hosts, err := h.Admin().Hosts(bg)
	if err != nil || len(hosts) != 1 || hosts[0].Name != "host-a" {
		t.Fatalf("%+v %v", hosts, err)
	}
	if got := hosts[0].Summary(); got != "host-a: 8c/8GB → 1 slot, throttled: cpu 85%" {
		t.Fatal(got)
	}
	// Load piggybacked on claim updates the same row.
	req := caps()
	req.Load = &api.HostLoad{Cores: 8, RAMGB: 8, Slots: 2}
	_, _ = h.Host("host-a").Claim(bg, req)
	if hosts, _ = h.Admin().Hosts(bg); hosts[0].Slots != 2 {
		t.Fatalf("%+v", hosts)
	}
	if err := h.Host("host-a").ReportLoad(bg, api.HostLoad{CPUPct: 500}); err == nil {
		t.Fatal("invalid load accepted")
	}
}

func TestListAndReportEndpoints(t *testing.T) {
	h := hubtest.Start(t)
	addTask(t, h)
	list, err := h.Admin().ListTasks(bg, api.StateQueued, time.Time{})
	if err != nil || len(list) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
	if rep, err := h.Admin().Report(bg, time.Time{}); err != nil || rep == "" {
		t.Fatalf("report: %q %v", rep, err)
	}
	if _, err := h.Admin().RequeueTask(bg, list[0].ID); err == nil {
		t.Fatal("requeue of queued task must conflict")
	}
}

func TestRestartKeepsLeasesViaGrace(t *testing.T) {
	h := hubtest.Start(t)
	task := addTask(t, h)
	host := h.Host("host-a")
	c, _ := host.Claim(bg, caps())
	if _, err := h.Store.Reap(bg); err != nil { // last_alive_at while the lease is live
		t.Fatal(err)
	}
	h.Clock.Advance(20 * time.Minute) // hub "down" past the 5m TTL
	h.Restart()                       // same URL, same database file, grace runs
	if err := host.Heartbeat(bg, c.ID, api.HeartbeatRequest{Fence: c.Fence}); err != nil {
		t.Fatalf("lease lost across restart: %v", err)
	}
	got, _ := h.Admin().GetTask(bg, task.ID)
	if got.State != api.StateRunning || got.Fence != c.Fence {
		t.Fatalf("%+v", got)
	}
}

func TestRestartWithoutGraceDoesNotReviveDeadLease(t *testing.T) {
	h := hubtest.Start(t)
	task := addTask(t, h)
	c, _ := h.Host("host-a").Claim(bg, caps())
	h.Clock.Advance(20 * time.Minute)
	if _, err := h.Store.Reap(bg); err != nil { // tick AFTER lease died
		t.Fatal(err)
	}
	h.Restart()
	// Lease expired 15 min ago: still within revive window, but hub did not grace it.
	got, _ := h.Admin().GetTask(bg, task.ID)
	if got.LeaseExpiresAt >= h.Clock.Now().UnixMilli() {
		t.Fatalf("dead lease extended: %+v", got)
	}
	_ = c
}
