package store_test

import (
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

func TestAddAppliesDefaultsAndValidates(t *testing.T) {
	e := newEnv(t)
	got := e.add(t, spec("r"))
	if got.State != api.StateQueued || got.BaseBranch != "main" || got.PermissionMode != "acceptEdits" ||
		got.ToolProfile != "default" || got.MaxAttempts != 3 || got.CreatedBy != "test" || got.CreatedAt == 0 {
		t.Fatalf("unexpected: %+v", got)
	}
	bad := spec("r")
	bad.PermissionMode = "bypassPermissions"
	if _, err := e.st.Add(bg, bad, "x"); !api.IsValidation(err) {
		t.Fatalf("want validation error, got %v", err)
	}
}

func TestGetNotFound(t *testing.T) {
	e := newEnv(t)
	_, err := e.st.Get(bg, 42)
	wantErr(t, err, store.ErrNotFound)
}

func TestListFilters(t *testing.T) {
	e := newEnv(t)
	a := e.add(t, spec("r"))
	e.clock.Advance(time.Hour)
	b := e.add(t, spec("r"))
	e.claim(t, "box") // a -> running

	all, _ := e.st.List(bg, "", time.Time{})
	if len(all) != 2 || all[0].ID != b.ID {
		t.Fatalf("want newest first: %+v", all)
	}
	running, _ := e.st.List(bg, api.StateRunning, time.Time{})
	if len(running) != 1 || running[0].ID != a.ID {
		t.Fatalf("running filter: %+v", running)
	}
	recent, _ := e.st.List(bg, "", e.clock.Now().Add(-time.Minute))
	if len(recent) != 2 { // claim touched a, add created b
		t.Fatalf("since filter: %d", len(recent))
	}
	e.clock.Advance(time.Hour)
	if none, _ := e.st.List(bg, "", e.clock.Now()); len(none) != 0 {
		t.Fatalf("since in future: %d", len(none))
	}
}

func TestListCap(t *testing.T) {
	e := newEnv(t)
	tx, err := e.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < store.ListLimit+20; i++ {
		if _, err := tx.Exec(`INSERT INTO tasks (repo,prompt,max_budget_usd,created_at,updated_at) VALUES ('r','p',1,1,1)`); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.List(bg, "", time.Time{})
	if len(got) != store.ListLimit {
		t.Fatalf("len=%d", len(got))
	}
}

func TestListForReport(t *testing.T) {
	e := newEnv(t)
	oldDone := e.add(t, spec("r"))
	c := e.claim(t, "box")
	if _, err := e.st.Complete(bg, "box", oldDone.ID, api.CompleteRequest{Fence: c.Fence}); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(48 * time.Hour)
	queued := e.add(t, spec("r"))
	got, err := e.st.ListForReport(bg, e.clock.Now().Add(-16*time.Hour))
	if err != nil || len(got) != 1 || got[0].ID != queued.ID {
		t.Fatalf("old done task must be excluded, queued included: %+v %v", got, err)
	}
	got, _ = e.st.ListForReport(bg, e.clock.Now().Add(-72*time.Hour))
	if len(got) != 2 {
		t.Fatalf("want both in wide window: %d", len(got))
	}
}

func TestCancelAndRequeue(t *testing.T) {
	e := newEnv(t)
	task := e.add(t, spec("r"))
	got, err := e.st.Cancel(bg, task.ID)
	if err != nil || got.State != api.StateCancelled || got.FinishedAt == 0 {
		t.Fatalf("%+v %v", got, err)
	}
	if e.claim(t, "box") != nil {
		t.Fatal("cancelled task claimed")
	}
	_, err = e.st.Cancel(bg, task.ID)
	wantErr(t, err, store.ErrConflict)

	got, err = e.st.Requeue(bg, task.ID)
	if err != nil || got.State != api.StateQueued || got.Attempts != 0 || got.FinishedAt != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	_, err = e.st.Requeue(bg, task.ID)
	wantErr(t, err, store.ErrConflict)
	_, err = e.st.Requeue(bg, 999)
	wantErr(t, err, store.ErrNotFound)
	if c := e.claim(t, "box"); c == nil || c.Fence != 1 {
		t.Fatalf("requeue must leave fence untouched until claim: %+v", c)
	}
}

func TestRequeueStuckResetsAttempts(t *testing.T) {
	e := newEnv(t)
	s := spec("r")
	s.MaxAttempts = 1
	task := e.add(t, s)
	c := e.claim(t, "box")
	if _, err := e.st.Fail(bg, "box", task.ID, api.FailRequest{Fence: c.Fence}); err != nil {
		t.Fatal(err)
	}
	got, err := e.st.Requeue(bg, task.ID)
	if err != nil || got.State != api.StateQueued || got.Attempts != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	if c2 := e.claim(t, "box"); c2 == nil || c2.Fence != c.Fence+1 {
		t.Fatalf("%+v", c2)
	}
}

func TestHostsUpsertAndList(t *testing.T) {
	e := newEnv(t)
	load := api.HostLoad{Cores: 8, RAMGB: 8, CPUModel: "cpu", Slots: 1, CPUPct: 85, ThrottleReason: "cpu 85%", AgentVersion: "dev"}
	if err := e.st.UpsertHostLoad(bg, "box", load); err != nil {
		t.Fatal(err)
	}
	load.Slots, load.ThrottleReason = 2, ""
	if err := e.st.UpsertHostLoad(bg, "box", load); err != nil {
		t.Fatal(err)
	}
	hosts, err := e.st.ListHosts(bg)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("%+v %v", hosts, err)
	}
	h := hosts[0]
	if h.Name != "box" || h.Slots != 2 || h.ThrottleReason != "" || h.LastSeenAt == 0 || h.Cores != 8 {
		t.Fatalf("%+v", h)
	}
	if err := e.st.UpsertHostLoad(bg, "Bad Name", load); !api.IsValidation(err) {
		t.Fatalf("bad name accepted: %v", err)
	}
	if err := e.st.UpsertHostLoad(bg, "box", api.HostLoad{CPUPct: 500}); !api.IsValidation(err) {
		t.Fatalf("bad load accepted: %v", err)
	}
}
