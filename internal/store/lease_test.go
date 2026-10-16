package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

var bg = context.Background()

func TestHeartbeatFence(t *testing.T) {
	e := newEnv(t)
	task := e.add(t, spec("r"))
	c := e.claim(t, "box")
	if err := e.st.Heartbeat(bg, "box", task.ID, c.Fence); err != nil {
		t.Fatal(err)
	}
	wantErr(t, e.st.Heartbeat(bg, "box", task.ID, c.Fence+1), store.ErrLeaseLost)
	wantErr(t, e.st.Heartbeat(bg, "other", task.ID, c.Fence), store.ErrLeaseLost)
	wantErr(t, e.st.Heartbeat(bg, "box", 9999, 1), store.ErrNotFound)
}

func TestHeartbeatExtendsLease(t *testing.T) {
	e := newEnv(t)
	task := e.add(t, spec("r"))
	c := e.claim(t, "box")
	before := e.get(t, task.ID).LeaseExpiresAt
	e.clock.Advance(time.Minute)
	if err := e.st.Heartbeat(bg, "box", task.ID, c.Fence); err != nil {
		t.Fatal(err)
	}
	if after := e.get(t, task.ID).LeaseExpiresAt; after != before+time.Minute.Milliseconds() {
		t.Fatalf("lease %d -> %d", before, after)
	}
}

func TestHeartbeatCancelled(t *testing.T) {
	e := newEnv(t)
	task := e.add(t, spec("r"))
	c := e.claim(t, "box")
	if _, err := e.st.Cancel(bg, task.ID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, e.st.Heartbeat(bg, "box", task.ID, c.Fence), store.ErrCancelled)
	_, err := e.st.Complete(bg, "box", task.ID, api.CompleteRequest{Fence: c.Fence})
	wantErr(t, err, store.ErrCancelled)
	wantErr(t, e.st.Heartbeat(bg, "intruder", task.ID, c.Fence), store.ErrLeaseLost)
}

func TestExpiredLeaseReclaimed(t *testing.T) {
	e := newEnv(t)
	task := e.add(t, spec("r"))
	old := e.claim(t, "slow")
	e.clock.Advance(6 * time.Minute) // past 5m TTL
	nw := e.claim(t, "fast")
	if nw == nil || nw.ID != task.ID || nw.Fence != old.Fence+1 {
		t.Fatalf("reclaim failed: %+v", nw)
	}
	got := e.get(t, task.ID)
	if got.Attempts != 2 || got.ClaimedBy != "fast" || got.Reason != "lease expired on slow" {
		t.Fatalf("unexpected row: %+v", got)
	}
	wantErr(t, e.st.Heartbeat(bg, "slow", task.ID, old.Fence), store.ErrLeaseLost)
	_, err := e.st.Complete(bg, "slow", task.ID, api.CompleteRequest{Fence: old.Fence})
	wantErr(t, err, store.ErrLeaseLost)
}

func TestHeartbeatRevivalWindow(t *testing.T) {
	e := newEnv(t)
	task := e.add(t, spec("r"))
	c := e.claim(t, "box")
	e.clock.Advance(5*time.Minute + 10*time.Minute) // expired 10 min ago
	if err := e.st.Heartbeat(bg, "box", task.ID, c.Fence); err != nil {
		t.Fatalf("revival within window failed: %v", err)
	}
	e.clock.Advance(5*time.Minute + 40*time.Minute) // expired 40 min ago
	wantErr(t, e.st.Heartbeat(bg, "box", task.ID, c.Fence), store.ErrLeaseLost)
}

func TestCompleteIdempotent(t *testing.T) {
	e := newEnv(t)
	task := e.add(t, spec("r"))
	c := e.claim(t, "box")
	req := api.CompleteRequest{Fence: c.Fence, Branch: "agentmesh/task-1-f1",
		PRURL: "https://github.com/o/r/pull/9", CostUSD: 1.25, SessionID: "s1"}
	again, err := e.st.Complete(bg, "box", task.ID, req)
	if err != nil || again {
		t.Fatalf("first complete: again=%v err=%v", again, err)
	}
	again, err = e.st.Complete(bg, "box", task.ID, req)
	if err != nil || !again {
		t.Fatalf("replay: again=%v err=%v", again, err)
	}
	got := e.get(t, task.ID)
	if got.State != api.StateDone || got.CostUSD != 1.25 || got.PRURL != req.PRURL || got.FinishedAt == 0 {
		t.Fatalf("replay changed or lost data: %+v", got)
	}
	// Wrong fence / wrong host never count as a replay.
	_, err = e.st.Complete(bg, "box", task.ID, api.CompleteRequest{Fence: c.Fence + 1})
	wantErr(t, err, store.ErrLeaseLost)
	_, err = e.st.Complete(bg, "other", task.ID, api.CompleteRequest{Fence: c.Fence})
	wantErr(t, err, store.ErrLeaseLost)
}

func TestCompleteRejectsBadPRURL(t *testing.T) {
	e := newEnv(t)
	task := e.add(t, spec("r"))
	c := e.claim(t, "box")
	_, err := e.st.Complete(bg, "box", task.ID, api.CompleteRequest{Fence: c.Fence, PRURL: "https://evil.example/x"})
	if !api.IsValidation(err) {
		t.Fatalf("want validation error, got %v", err)
	}
}

func TestFailRetryAndCap(t *testing.T) {
	e := newEnv(t)
	s := spec("r")
	s.MaxAttempts = 2
	task := e.add(t, s)

	c1 := e.claim(t, "box")
	again, err := e.st.Fail(bg, "box", task.ID, api.FailRequest{Fence: c1.Fence, Retry: true, CostUSD: 1, Reason: "boom"})
	if err != nil || again {
		t.Fatal(again, err)
	}
	if got := e.get(t, task.ID); got.State != api.StateQueued || got.CostUSD != 1 || got.Reason != "boom" {
		t.Fatalf("retry below cap: %+v", got)
	}
	again, err = e.st.Fail(bg, "box", task.ID, api.FailRequest{Fence: c1.Fence, Retry: true, CostUSD: 1})
	if err != nil || !again {
		t.Fatalf("replay: %v %v", again, err)
	}
	if got := e.get(t, task.ID); got.CostUSD != 1 {
		t.Fatalf("replay double-counted cost: %v", got.CostUSD)
	}

	c2 := e.claim(t, "box") // attempt 2 of 2
	if _, err := e.st.Fail(bg, "box", task.ID, api.FailRequest{Fence: c2.Fence, Retry: true, CostUSD: 0.5}); err != nil {
		t.Fatal(err)
	}
	got := e.get(t, task.ID)
	if got.State != api.StateStuck || got.CostUSD != 1.5 || got.FinishedAt == 0 {
		t.Fatalf("retry at cap must stick: %+v", got)
	}
}

func TestFailWithoutRetryIsStuck(t *testing.T) {
	e := newEnv(t)
	task := e.add(t, spec("r"))
	c := e.claim(t, "box")
	if _, err := e.st.Fail(bg, "box", task.ID, api.FailRequest{Fence: c.Fence}); err != nil {
		t.Fatal(err)
	}
	if got := e.get(t, task.ID); got.State != api.StateStuck {
		t.Fatalf("state=%s", got.State)
	}
	again, err := e.st.Fail(bg, "box", task.ID, api.FailRequest{Fence: c.Fence})
	if err != nil || !again {
		t.Fatalf("replay: %v %v", again, err)
	}
}

func TestReleaseKeepsAttemptAndIsIdempotent(t *testing.T) {
	e := newEnv(t)
	task := e.add(t, spec("r"))
	c := e.claim(t, "box")
	again, err := e.st.Release(bg, "box", task.ID, api.ReleaseRequest{Fence: c.Fence, Reason: "profile mismatch"})
	if err != nil || again {
		t.Fatal(again, err)
	}
	got := e.get(t, task.ID)
	if got.State != api.StateQueued || got.Attempts != 0 || got.Reason != "released by box: profile mismatch" {
		t.Fatalf("release: %+v", got)
	}
	again, err = e.st.Release(bg, "box", task.ID, api.ReleaseRequest{Fence: c.Fence})
	if err != nil || !again {
		t.Fatalf("replay: %v %v", again, err)
	}
	// Another host takes it; the old holder's replay is now a lease loss.
	c2 := e.claim(t, "other")
	if c2.Fence != c.Fence+1 {
		t.Fatalf("fence %d", c2.Fence)
	}
	_, err = e.st.Release(bg, "box", task.ID, api.ReleaseRequest{Fence: c.Fence})
	wantErr(t, err, store.ErrLeaseLost)
}
