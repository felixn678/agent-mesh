package store_test

import (
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

func TestReapStuckAtAttemptCap(t *testing.T) {
	e := newEnv(t)
	s := spec("r")
	s.MaxAttempts = 1
	task := e.add(t, s)
	e.claim(t, "box")

	res, err := e.st.Reap(bg)
	if err != nil || res.Stuck != 0 {
		t.Fatalf("live lease reaped: %+v %v", res, err)
	}
	e.clock.Advance(6 * time.Minute) // expired, but the owner may still revive it
	if res, err = e.st.Reap(bg); err != nil || res.Stuck != 0 {
		t.Fatalf("reaped inside the revive window: %+v %v", res, err)
	}
	e.clock.Advance(30 * time.Minute) // expired 31m: past the 30m revive window
	if res, err = e.st.Reap(bg); err != nil || res.Stuck != 1 {
		t.Fatalf("want 1 stuck: %+v %v", res, err)
	}
	got := e.get(t, task.ID)
	if got.State != api.StateStuck || got.Reason != "lease expired on box at attempt cap" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestReapStaleRequeueBumpsFence(t *testing.T) {
	e := newEnv(t)
	task := e.add(t, spec("r"))
	c := e.claim(t, "box")

	e.clock.Advance(5*time.Minute + 30*time.Minute) // expired 30m: below the 1h stale line
	if res, _ := e.st.Reap(bg); res.Requeued != 0 {
		t.Fatalf("requeued too early: %+v", res)
	}
	e.clock.Advance(40 * time.Minute) // expired 70m
	res, err := e.st.Reap(bg)
	if err != nil || res.Requeued != 1 {
		t.Fatalf("want 1 requeued: %+v %v", res, err)
	}
	got := e.get(t, task.ID)
	if got.State != api.StateQueued || got.Fence != c.Fence+1 || got.Reason != "lease expired on box; requeued" {
		t.Fatalf("unexpected: %+v", got)
	}
	wantErr(t, e.st.Heartbeat(bg, "box", task.ID, c.Fence), store.ErrLeaseLost)
	_, err = e.st.Complete(bg, "box", task.ID, api.CompleteRequest{Fence: c.Fence})
	wantErr(t, err, store.ErrLeaseLost)
	// The next claim continues the fence sequence without reuse.
	if c2 := e.claim(t, "other"); c2 == nil || c2.Fence != c.Fence+2 {
		t.Fatalf("next claim: %+v", c2)
	}
}

func TestGraceOnlyLeasesAliveAtCrash(t *testing.T) {
	e := newEnv(t)
	a := e.add(t, spec("r"))
	cA := e.claim(t, "box") // lease until T+5m
	e.clock.Advance(4 * time.Minute)
	b := e.add(t, spec("r"))
	e.claim(t, "box") // lease until T+9m
	e.clock.Advance(time.Minute)
	if _, err := e.st.Reap(bg); err != nil { // last_alive_at = T+5m; lease A expires exactly now: still >= alive
		t.Fatal(err)
	}
	e.clock.Advance(30 * time.Minute) // hub down; both leases long expired
	// Task c leased by a long-dead worker before the crash: expired before last_alive_at.
	n, err := e.st.Grace(bg)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("graced %d leases, want 2", n)
	}
	for _, id := range []int64{a.ID, b.ID} {
		if got := e.get(t, id); got.LeaseExpiresAt != e.clock.Now().Add(5*time.Minute).UnixMilli() {
			t.Fatalf("task %d lease not extended: %d", id, got.LeaseExpiresAt)
		}
	}
	if err := e.st.Heartbeat(bg, "box", a.ID, cA.Fence); err != nil {
		t.Fatalf("worker lost its lease across restart: %v", err)
	}
}

func TestGraceSkipsLeasesDeadBeforeCrash(t *testing.T) {
	e := newEnv(t)
	task := e.add(t, spec("r"))
	e.claim(t, "box")
	e.clock.Advance(10 * time.Minute) // lease died long before the final tick
	if _, err := e.st.Reap(bg); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	if n, err := e.st.Grace(bg); err != nil || n != 0 {
		t.Fatalf("grace revived a dead lease: n=%d err=%v", n, err)
	}
	if got := e.get(t, task.ID); got.LeaseExpiresAt >= e.clock.Now().UnixMilli() {
		t.Fatal("dead lease was extended")
	}
}

func TestGraceOncePerDowntime(t *testing.T) {
	e := newEnv(t)
	e.add(t, spec("r"))
	e.claim(t, "box")
	if _, err := e.st.Reap(bg); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	if n, _ := e.st.Grace(bg); n != 1 {
		t.Fatalf("first start graced %d", n)
	}
	// Crash loop: restarts before any reaper tick must not extend again.
	e.clock.Advance(4 * time.Minute)
	if n, err := e.st.Grace(bg); err != nil || n != 0 {
		t.Fatalf("second start re-graced: n=%d err=%v", n, err)
	}
}

func TestGraceNoopOnFreshDatabase(t *testing.T) {
	e := newEnv(t)
	if n, err := e.st.Grace(bg); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestAutoincrementNeverReusesIDs(t *testing.T) {
	e := newEnv(t)
	last := e.add(t, spec("r"))
	if _, err := e.db.Exec("DELETE FROM tasks WHERE id=?", last.ID); err != nil {
		t.Fatal(err)
	}
	if next := e.add(t, spec("r")); next.ID <= last.ID {
		t.Fatalf("id reused: %d after deleted %d", next.ID, last.ID)
	}
}
