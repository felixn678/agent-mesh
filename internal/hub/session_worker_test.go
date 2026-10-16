package hub_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/hubtest"
)

const project = "github.com/owner/proj"

func regSession(t *testing.T, c *client.Client, id string) {
	t.Helper()
	if _, err := c.RegisterSession(bg, api.RegisterSessionRequest{ID: id, Repo: "git@github.com:Owner/Proj.git"}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionWorkerLifecycleAndCompletionNotice(t *testing.T) {
	h := hubtest.Start(t)
	boss, worker, other := h.Session("host-a"), h.Session("host-b"), h.Session("host-b")
	regSession(t, boss, "boss")
	regSession(t, worker, "w1")
	regSession(t, other, "w2")

	task, err := boss.AddTask(bg, api.TaskSpec{Repo: "https://github.com/owner/proj.git", Prompt: "do it", MaxBudgetUSD: 1,
		TargetHost: "host-b", CreatedBySession: "boss"})
	if err != nil || task.CreatedBySession != "boss" || task.Repo != project || task.TargetHost != "host-b" {
		t.Fatalf("%+v %v", task, err)
	}
	// The boss's own host cannot claim a host-b task.
	if _, err := boss.SessionClaim(bg, "boss"); !errors.Is(err, client.ErrNoTask) {
		t.Fatalf("boss claim: %v", err)
	}
	c, err := worker.SessionClaim(bg, "w1")
	if err != nil || c.ID != task.ID || c.Fence != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := other.SessionClaim(bg, "w2"); !errors.Is(err, client.ErrNoTask) {
		t.Fatalf("second claim: %v", err)
	}
	// w2 cannot heartbeat/complete/fail/release w1's task.
	if err := other.SessionHeartbeat(bg, "w2", task.ID, api.HeartbeatRequest{Fence: c.Fence}); !client.IsLeaseLost(err) {
		t.Fatalf("heartbeat: %v", err)
	}
	if _, err := other.SessionComplete(bg, "w2", task.ID, api.CompleteRequest{Fence: c.Fence}); !client.IsLeaseLost(err) {
		t.Fatalf("complete: %v", err)
	}
	if _, err := other.SessionFail(bg, "w2", task.ID, api.FailRequest{Fence: c.Fence}); !client.IsLeaseLost(err) {
		t.Fatalf("fail: %v", err)
	}
	if _, err := other.SessionRelease(bg, "w2", task.ID, api.ReleaseRequest{Fence: c.Fence}); !client.IsLeaseLost(err) {
		t.Fatalf("release: %v", err)
	}
	// A session of another host cannot act in w1's name.
	if err := boss.SessionHeartbeat(bg, "w1", task.ID, api.HeartbeatRequest{Fence: c.Fence}); httpStatus(err) != 403 {
		t.Fatalf("cross-host: %v", err)
	}
	// Daemon token (host-b) cannot use the session routes.
	if _, err := h.Host("host-b").SessionClaim(bg, "w1"); httpStatus(err) != 403 {
		t.Fatalf("daemon token: %v", err)
	}
	if err := worker.SessionHeartbeat(bg, "w1", task.ID, api.HeartbeatRequest{Fence: c.Fence}); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.SessionComplete(bg, "w1", task.ID, api.CompleteRequest{Fence: c.Fence,
		Branch: "feat/x", PRURL: "https://github.com/owner/proj/pull/3", Summary: strings.Repeat("r", 1500)}); err != nil {
		t.Fatal(err)
	}
	if again, err := worker.SessionComplete(bg, "w1", task.ID, api.CompleteRequest{Fence: c.Fence}); err != nil || !again {
		t.Fatalf("replay: %v %v", again, err)
	}
	msgs, err := boss.Inbox(bg, "boss", true)
	if err != nil || len(msgs) != 1 || msgs[0].FromSession != "hub" {
		t.Fatalf("%+v %v", msgs, err)
	}
	for _, want := range []string{"done", "feat/x", "pull/3", "result: rrr"} {
		if !strings.Contains(msgs[0].Body, want) {
			t.Errorf("notice missing %q: %q", want, msgs[0].Body)
		}
	}
	if len(msgs[0].Body) > 2200 {
		t.Errorf("notice too long: %d", len(msgs[0].Body))
	}
}

func TestCreatorSessionMustBelongToCallerHost(t *testing.T) {
	h := hubtest.Start(t)
	regSession(t, h.Session("host-a"), "boss")
	spec := api.TaskSpec{Repo: "r", Prompt: "p", MaxBudgetUSD: 1, CreatedBySession: "boss"}
	if _, err := h.Session("host-b").AddTask(bg, spec); httpStatus(err) != 403 {
		t.Fatalf("foreign creator: %v", err)
	}
}

func TestWaiterWakesOnClaimableTaskWithin1s(t *testing.T) {
	h := hubtest.Start(t)
	w := h.Session("host-a")
	regSession(t, w, "idle")
	type res struct {
		r   api.InboxResponse
		err error
	}
	ch := make(chan res, 1)
	go func() {
		r, err := w.InboxWaitClaimable(bg, "idle", true, 20*time.Second)
		ch <- res{r, err}
	}()
	time.Sleep(300 * time.Millisecond) // let the waiter park
	start := time.Now()
	if _, err := h.Admin().AddTask(bg, api.TaskSpec{Repo: project, Prompt: "p", MaxBudgetUSD: 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-ch:
		if got.err != nil || got.r.ClaimableCount != 1 || len(got.r.Messages) != 0 {
			t.Fatalf("%+v %v", got.r, got.err)
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("woke after %v", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter did not wake")
	}
	// Waking did not claim anything.
	if tasks, _ := h.Admin().ListTasks(bg, "queued", time.Time{}); len(tasks) != 1 {
		t.Fatalf("queued: %+v", tasks)
	}
	// A task for another project or session does not count; a plain wait times out empty.
	other := api.TaskSpec{Repo: "github.com/owner/else", Prompt: "p", MaxBudgetUSD: 1}
	_, _ = h.Admin().AddTask(bg, other)
	if _, err := w.SessionClaim(bg, "idle"); err != nil {
		t.Fatal(err)
	}
	r, err := w.InboxWaitClaimable(bg, "idle", true, 1*time.Second)
	if err != nil || r.ClaimableCount != 0 || len(r.Messages) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestWaiterWakesOnCompletionNotice(t *testing.T) {
	h := hubtest.Start(t)
	boss, worker := h.Session("host-a"), h.Session("host-b")
	regSession(t, boss, "boss")
	regSession(t, worker, "w1")
	task, err := boss.AddTask(bg, api.TaskSpec{Repo: project, Prompt: "p", MaxBudgetUSD: 1, CreatedBySession: "boss"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := worker.SessionClaim(bg, "w1")
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan []api.Message, 1)
	go func() {
		m, _ := boss.InboxWait(bg, "boss", true, 20*time.Second)
		ch <- m
	}()
	time.Sleep(300 * time.Millisecond)
	if _, err := worker.SessionFail(bg, "w1", task.ID, api.FailRequest{Fence: c.Fence, Reason: "nope"}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-ch:
		if len(m) != 1 || !strings.Contains(m[0].Body, "stuck") {
			t.Fatalf("%+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("creator not woken")
	}
}

func TestSessionLeaseLongerThanDaemonLease(t *testing.T) {
	h := hubtest.Start(t)
	w := h.Session("host-a")
	regSession(t, w, "w1")
	task, _ := h.Admin().AddTask(bg, api.TaskSpec{Repo: project, Prompt: "p", MaxBudgetUSD: 1})
	c, err := w.SessionClaim(bg, "w1")
	if err != nil {
		t.Fatal(err)
	}
	h.Clock.Advance(20 * time.Minute) // past the 5 min daemon TTL, within 30 min
	if _, err := h.Host("host-a").Claim(bg, api.ClaimRequest{Repos: []string{project},
		Modes: []string{"acceptEdits"}, Profiles: []string{"default"}, BudgetCap: 5}); !errors.Is(err, client.ErrNoTask) {
		t.Fatalf("lease stolen: %v", err)
	}
	if err := w.SessionHeartbeat(bg, "w1", task.ID, api.HeartbeatRequest{Fence: c.Fence}); err != nil {
		t.Fatal(err)
	}
}
