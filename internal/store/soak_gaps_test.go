package store_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

func addAs(t *testing.T, e *env, p api.Principal, rid, prompt string) (api.Task, bool, error) {
	t.Helper()
	s := spec("r")
	s.Prompt, s.RequestID = prompt, rid
	return e.st.AddTaskReplay(context.Background(), s, p)
}

func countTasks(t *testing.T, e *env) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow("SELECT COUNT(*) FROM tasks").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTaskAddRequestIDIsIdempotent(t *testing.T) {
	e := newEnv(t)
	a := api.Principal{Host: "host-a", Session: true}
	first, replayed, err := addAs(t, e, a, "add-1", "fix it")
	if err != nil || replayed || first.RequestID != "add-1" {
		t.Fatalf("first add: %+v %v %v", first, replayed, err)
	}
	again, replayed, err := addAs(t, e, a, "add-1", "fix it") // response was lost; retry
	if err != nil || !replayed || again.ID != first.ID {
		t.Fatalf("retry: %+v replayed=%v %v", again, replayed, err)
	}
	if n := countTasks(t, e); n != 1 {
		t.Fatalf("retry added a duplicate: %d tasks", n)
	}
	// Another principal may use the same id for its own task.
	other, replayed, err := addAs(t, e, api.Principal{Host: "host-b", Session: true}, "add-1", "fix it")
	if err != nil || replayed || other.ID == first.ID {
		t.Fatalf("other principal: %+v %v %v", other, replayed, err)
	}
	// Reusing an id for different work is a conflict, not a silent replay.
	_, _, err = addAs(t, e, a, "add-1", "something else")
	wantErr(t, err, store.ErrConflict)
	// Without an id every add is new.
	addAs(t, e, a, "", "x")
	addAs(t, e, a, "", "x")
	if n := countTasks(t, e); n != 4 {
		t.Fatalf("want 4 tasks, got %d", n)
	}
	if _, _, err := addAs(t, e, a, "bad id!", "x"); !api.IsValidation(err) {
		t.Fatalf("invalid request id: %v", err)
	}
}

func TestConcurrentRetriedAddsMakeOneTask(t *testing.T) {
	e := newEnv(t)
	a := api.Principal{Admin: true}
	var wg sync.WaitGroup
	ids := make(chan int64, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if task, _, err := addAs(t, e, a, "same", "p"); err == nil {
				ids <- task.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[int64]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 || countTasks(t, e) != 1 {
		t.Fatalf("want one task, got ids %v and %d rows", seen, countTasks(t, e))
	}
}

func targetedTask(t *testing.T, e *env, target string) api.Task {
	t.Helper()
	return e.add(t, api.TaskSpec{Repo: proj, Prompt: "p", MaxBudgetUSD: 1, TargetSession: target, CreatedBySession: "boss"})
}

func TestReaperSticksTasksForGoneTargetSessions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, id := range []string{"boss", "ended", "silent", "alive"} {
		e.session(t, "host-a", id, proj)
	}
	toEnded, toSilent := targetedTask(t, e, "ended"), targetedTask(t, e, "silent")
	toAlive, toNobody := targetedTask(t, e, "alive"), targetedTask(t, e, "never-registered")
	if err := e.st.EndSession(ctx, "host-a", api.EndSessionRequest{ID: "ended"}); err != nil {
		t.Fatal(err)
	}
	if r, err := e.st.Reap(ctx); err != nil || r.TargetGone != 1 {
		t.Fatalf("first reap: %+v %v", r, err)
	}
	if got := e.get(t, toEnded.ID); got.State != api.StateStuck || got.Reason != "target session gone" {
		t.Fatalf("task for ended session: %+v", got)
	}
	for _, id := range []int64{toSilent.ID, toAlive.ID, toNobody.ID} {
		if got := e.get(t, id); got.State != api.StateQueued {
			t.Fatalf("task %d reaped too early: %+v", id, got)
		}
	}

	// 13 h later: "silent" never came back and the unregistered target is
	// past the alive window too; "alive" and "boss" kept heartbeating.
	e.clock.Advance(13 * time.Hour)
	e.session(t, "host-a", "alive", proj)
	e.session(t, "host-a", "boss", proj)
	if r, err := e.st.Reap(ctx); err != nil || r.TargetGone != 2 {
		t.Fatalf("second reap: %+v %v", r, err)
	}
	for id, want := range map[int64]string{toSilent.ID: api.StateStuck, toNobody.ID: api.StateStuck, toAlive.ID: api.StateQueued} {
		if got := e.get(t, id); got.State != want {
			t.Fatalf("task %d: state %s, want %s", id, got.State, want)
		}
	}
	msgs, err := e.st.Inbox(ctx, "host-a", "boss", true)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("creator notices: %d %v", len(msgs), err)
	}
	for _, m := range msgs {
		if !strings.Contains(m.Body, "stuck") || !strings.Contains(m.Body, "result: target session gone") {
			t.Fatalf("notice %q", m.Body)
		}
	}
}

// A running task whose session went away is the lease reaper's business.
func TestTargetGoneLeavesRunningTasksAlone(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.session(t, "host-a", "boss", proj)
	e.session(t, "host-a", "w", proj)
	task := targetedTask(t, e, "w")
	if c, err := e.st.ClaimForSession(ctx, "host-a", "w"); err != nil || c == nil || c.ID != task.ID {
		t.Fatalf("claim: %+v %v", c, err)
	}
	if err := e.st.EndSession(ctx, "host-a", api.EndSessionRequest{ID: "w"}); err != nil {
		t.Fatal(err)
	}
	if r, err := e.st.Reap(ctx); err != nil || r.TargetGone != 0 {
		t.Fatalf("reap: %+v %v", r, err)
	}
	if got := e.get(t, task.ID); got.State != api.StateRunning {
		t.Fatalf("running task touched: %+v", got)
	}
}
