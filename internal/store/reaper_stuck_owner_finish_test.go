package store_test

import (
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

// reapedAtCap returns a single-attempt task whose lease the reaper turned
// stuck after the owner went silent past TTL + ReviveWindow.
func reapedAtCap(t *testing.T, e *env) *api.ClaimedTask {
	t.Helper()
	s := spec("r")
	s.MaxAttempts = 1
	e.add(t, s)
	c := e.claim(t, "box")
	e.clock.Advance(5*time.Minute + 31*time.Minute)
	if res, err := e.st.Reap(bg); err != nil || res.Stuck != 1 {
		t.Fatalf("reap: %+v %v", res, err)
	}
	return c
}

func TestReapAtCapLeavesRevivableLeaseAlone(t *testing.T) {
	e := newEnv(t)
	s := spec("r")
	s.MaxAttempts = 1
	e.add(t, s)
	c := e.claim(t, "box")
	e.clock.Advance(5*time.Minute + 2*time.Second) // one heartbeat slightly late
	if res, err := e.st.Reap(bg); err != nil || res.Stuck != 0 {
		t.Fatalf("late heartbeat at the cap reaped: %+v %v", res, err)
	}
	if err := e.st.Heartbeat(bg, "box", c.ID, c.Fence); err != nil {
		t.Fatalf("owner revive: %v", err)
	}
	if got := e.get(t, c.ID); got.State != api.StateRunning {
		t.Fatalf("state %s", got.State)
	}
}

func TestOwnerFailAfterReapRecordsItsOutcome(t *testing.T) {
	e := newEnv(t)
	c := reapedAtCap(t, e)
	req := api.FailRequest{Fence: c.Fence, CostUSD: 1.5, Reason: "secret scan blocked", Worktree: "/wt/1", SessionID: "s-1"}
	again, err := e.st.Fail(bg, "box", c.ID, req)
	if err != nil || again {
		t.Fatalf("late fail: again=%v err=%v", again, err)
	}
	got := e.get(t, c.ID)
	if got.State != api.StateStuck || got.Reason != "secret scan blocked" || got.CostUSD != 1.5 ||
		got.Worktree != "/wt/1" || got.SessionID != "s-1" {
		t.Fatalf("outcome lost: %+v", got)
	}
	// Replay is idempotent and does not add the cost twice.
	if again, err = e.st.Fail(bg, "box", c.ID, req); err != nil || !again {
		t.Fatalf("replay: again=%v err=%v", again, err)
	}
	if got = e.get(t, c.ID); got.CostUSD != 1.5 {
		t.Fatalf("cost doubled: %v", got.CostUSD)
	}
	// Once the worker's own failure is recorded, a later Complete is refused.
	_, err = e.st.Complete(bg, "box", c.ID, api.CompleteRequest{Fence: c.Fence})
	wantErr(t, err, store.ErrLeaseLost)
}

func TestOwnerCompleteAfterReapWinsOthersDoNot(t *testing.T) {
	e := newEnv(t)
	c := reapedAtCap(t, e)
	_, err := e.st.Complete(bg, "other", c.ID, api.CompleteRequest{Fence: c.Fence})
	wantErr(t, err, store.ErrLeaseLost)
	_, err = e.st.Complete(bg, "box", c.ID, api.CompleteRequest{Fence: c.Fence + 1})
	wantErr(t, err, store.ErrLeaseLost)
	_, err = e.st.Release(bg, "box", c.ID, api.ReleaseRequest{Fence: c.Fence}) // no revival through release
	wantErr(t, err, store.ErrLeaseLost)
	if err := e.st.Heartbeat(bg, "box", c.ID, c.Fence); err == nil {
		t.Fatal("heartbeat revived a reaped task")
	}

	done := api.CompleteRequest{Fence: c.Fence, Branch: "agentmesh/1-x", PRURL: "https://github.com/o/r/pull/1", CostUSD: 0.4}
	if again, err := e.st.Complete(bg, "box", c.ID, done); err != nil || again {
		t.Fatalf("late complete: again=%v err=%v", again, err)
	}
	got := e.get(t, c.ID)
	if got.State != api.StateDone || got.PRURL != done.PRURL || got.CostUSD != 0.4 || got.LeaseExpiresAt != 0 {
		t.Fatalf("%+v", got)
	}
	if again, err := e.st.Complete(bg, "box", c.ID, done); err != nil || !again {
		t.Fatalf("replay: again=%v err=%v", again, err)
	}
}

func TestCancelOrRequeueEndsLateFinish(t *testing.T) {
	e := newEnv(t)
	c := reapedAtCap(t, e)
	if _, err := e.st.Cancel(bg, c.ID); err != nil {
		t.Fatal(err)
	}
	_, err := e.st.Complete(bg, "box", c.ID, api.CompleteRequest{Fence: c.Fence})
	wantErr(t, err, store.ErrCancelled)
	if _, err := e.st.Requeue(bg, c.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.st.Fail(bg, "box", c.ID, api.FailRequest{Fence: c.Fence, Reason: "late"})
	wantErr(t, err, store.ErrLeaseLost)
}
