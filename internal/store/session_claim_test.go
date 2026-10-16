package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

const proj = "github.com/owner/proj"

func (e *env) session(t *testing.T, host, id, repo string) {
	t.Helper()
	if _, err := e.st.RegisterSession(context.Background(), host,
		api.RegisterSessionRequest{ID: id, Repo: repo}); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterSessionNormalizesRepoAndKeepsItOnRefresh(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.session(t, "host-a", "s1", "git@GitHub.com:Owner/Proj.git")
	got, err := e.st.RegisterSession(ctx, "host-a", api.RegisterSessionRequest{ID: "s1"})
	if err != nil || got.Repo != proj {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := e.st.RegisterSession(ctx, "host-a", api.RegisterSessionRequest{ID: "s2", Repo: "nonsense"}); !api.IsValidation(err) {
		t.Fatalf("bad repo: %v", err)
	}
}

func TestTaskRepoStoredNormalizedAndComparable(t *testing.T) {
	e := newEnv(t)
	task := e.add(t, spec("https://github.com/Owner/Proj.git"))
	if task.Repo != proj {
		t.Fatalf("repo %q", task.Repo)
	}
	// A daemon listing the repo in any URL form claims it.
	c, err := e.st.Claim(context.Background(), "host-a", api.ClaimRequest{Repos: []string{"git@github.com:owner/proj"},
		Modes: []string{"acceptEdits"}, Profiles: []string{"default"}, BudgetCap: 10})
	if err != nil || c == nil || c.ID != task.ID {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestSessionClaimFiltersByRepoAndTargets(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.session(t, "host-a", "s1", proj)
	e.session(t, "host-a", "s2", proj)
	e.session(t, "host-b", "s3", proj)
	e.session(t, "host-a", "s4", "github.com/owner/other")
	e.session(t, "host-a", "norepo", "")

	other := e.add(t, spec("github.com/owner/other"))
	toHostB := spec(proj)
	toHostB.TargetHost = "host-b"
	tb := e.add(t, toHostB)
	toS2 := spec(proj)
	toS2.TargetSession = "s2"
	t2 := e.add(t, toS2)
	free := e.add(t, spec(proj))

	// s1 (host-a): not host-b's task, not s2's task -> the free one.
	c, err := e.st.ClaimForSession(ctx, "host-a", "s1")
	if err != nil || c == nil || c.ID != free.ID {
		t.Fatalf("s1: %+v %v (free=%d)", c, err, free.ID)
	}
	if again, _ := e.st.ClaimForSession(ctx, "host-a", "s1"); again != nil {
		t.Fatalf("s1 got a second task: %+v", again)
	}
	// s2 gets the one targeted at it; s3 (host-b) gets the host-b one.
	if c, err := e.st.ClaimForSession(ctx, "host-a", "s2"); err != nil || c == nil || c.ID != t2.ID {
		t.Fatalf("s2: %+v %v", c, err)
	}
	if c, err := e.st.ClaimForSession(ctx, "host-b", "s3"); err != nil || c == nil || c.ID != tb.ID {
		t.Fatalf("s3: %+v %v", c, err)
	}
	// Other project only for s4; repo-less sessions get nothing.
	if c, err := e.st.ClaimForSession(ctx, "host-a", "norepo"); err != nil || c != nil {
		t.Fatalf("norepo: %+v %v", c, err)
	}
	if c, err := e.st.ClaimForSession(ctx, "host-a", "s4"); err != nil || c == nil || c.ID != other.ID {
		t.Fatalf("s4: %+v %v", c, err)
	}
	got := e.get(t, free.ID)
	if got.ClaimedBy != "session:s1" || got.State != api.StateRunning || got.Fence != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestSessionClaimRejectsBadSessions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.session(t, "host-a", "s1", proj)
	e.add(t, spec(proj))
	if _, err := e.st.ClaimForSession(ctx, "host-a", "ghost"); err == nil || err != store.ErrSessionNotFound {
		t.Fatalf("unknown: %v", err)
	}
	wantErr(t, func() error { _, err := e.st.ClaimForSession(ctx, "host-b", "s1"); return err }(), store.ErrSessionOwner)
	e.clock.Advance(13 * time.Hour) // past the alive window
	wantErr(t, func() error { _, err := e.st.ClaimForSession(ctx, "host-a", "s1"); return err }(), store.ErrSessionEnded)
	e.session(t, "host-a", "s1", proj) // refresh revives
	if err := e.st.EndSession(ctx, "host-a", api.EndSessionRequest{ID: "s1"}); err != nil {
		t.Fatal(err)
	}
	wantErr(t, func() error { _, err := e.st.ClaimForSession(ctx, "host-a", "s1"); return err }(), store.ErrSessionEnded)
}

func TestDaemonClaimIgnoresSessionTargetsAndHonoursTargetHost(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	toSession := spec("r")
	toSession.TargetSession = "s1"
	e.add(t, toSession)
	if c := e.claim(t, "host-a"); c != nil {
		t.Fatalf("daemon took a session-targeted task: %+v", c)
	}
	toB := spec("r")
	toB.TargetHost = "host-b"
	tb := e.add(t, toB)
	if c := e.claim(t, "host-a"); c != nil {
		t.Fatalf("host-a took host-b's task: %+v", c)
	}
	if c := e.claim(t, "host-b"); c == nil || c.ID != tb.ID {
		t.Fatalf("host-b: %+v", c)
	}
	_ = ctx
}

func TestSessionLeaseUsesLongTTLAndHolderIsolation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.session(t, "host-a", "s1", proj)
	e.session(t, "host-a", "s2", proj)
	task := e.add(t, spec(proj))
	c, _ := e.st.ClaimForSession(ctx, "host-a", "s1")
	if c == nil {
		t.Fatal("no claim")
	}
	if got := e.get(t, task.ID); got.LeaseExpiresAt-e.clock.Now().UnixMilli() != store.DefaultSessionTTL.Milliseconds() {
		t.Fatalf("lease %d", got.LeaseExpiresAt)
	}
	// Another session (and a daemon host) cannot touch it.
	wantErr(t, e.st.Heartbeat(ctx, "session:s2", task.ID, c.Fence), store.ErrLeaseLost)
	_, err := e.st.Complete(ctx, "session:s2", task.ID, api.CompleteRequest{Fence: c.Fence})
	wantErr(t, err, store.ErrLeaseLost)
	_, err = e.st.Fail(ctx, "host-a", task.ID, api.FailRequest{Fence: c.Fence})
	wantErr(t, err, store.ErrLeaseLost)
	// Heartbeat extends by the session TTL from the new time.
	e.clock.Advance(20 * time.Minute)
	if err := e.st.Heartbeat(ctx, "session:s1", task.ID, c.Fence); err != nil {
		t.Fatal(err)
	}
	if got := e.get(t, task.ID); got.LeaseExpiresAt-e.clock.Now().UnixMilli() != store.DefaultSessionTTL.Milliseconds() {
		t.Fatalf("lease after heartbeat %d", got.LeaseExpiresAt)
	}
	if _, err := e.st.Complete(ctx, "session:s1", task.ID, api.CompleteRequest{Fence: c.Fence, Summary: "ok"}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionClaimExactlyOnceAcross16Sessions(t *testing.T) {
	const tasks, sessions = 60, 16
	path := filepath.Join(t.TempDir(), "hub.db")
	var stores []*store.Store
	for i := 0; i < 4; i++ {
		db, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		stores = append(stores, store.New(db, nil, store.DefaultLeaseConfig()))
	}
	ctx := context.Background()
	for i := 0; i < sessions; i++ {
		if _, err := stores[0].RegisterSession(ctx, "host-a", api.RegisterSessionRequest{ID: fmt.Sprintf("s%d", i), Repo: proj}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < tasks; i++ {
		if _, err := stores[0].Add(ctx, api.TaskSpec{Repo: proj, Prompt: "p", MaxBudgetUSD: 1}, "x"); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	perTask := map[int64]string{}
	var wg sync.WaitGroup
	errs := make(chan error, sessions)
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("s%d", i)
			for {
				c, err := stores[i%4].ClaimForSession(ctx, "host-a", id)
				if err != nil {
					errs <- err
					return
				}
				if c == nil {
					return
				}
				mu.Lock()
				if prev, dup := perTask[c.ID]; dup {
					errs <- fmt.Errorf("task %d claimed by %s and %s", c.ID, prev, id)
				}
				perTask[c.ID] = id
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if len(perTask) != tasks {
		t.Fatalf("claimed %d of %d", len(perTask), tasks)
	}
}

func TestCompletionNoticeGoesToCreatorSession(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.session(t, "host-a", "boss", proj)
	mk := func() api.Task {
		s := spec("r")
		s.CreatedBySession = "boss"
		return e.add(t, s)
	}
	done, failed, retried, stuck := mk(), mk(), mk(), mk()
	c1 := e.claim(t, "host-a")
	c2 := e.claim(t, "host-a")
	c3 := e.claim(t, "host-a")
	c4 := e.claim(t, "host-a")
	if _, err := e.st.Complete(ctx, "host-a", done.ID, api.CompleteRequest{Fence: c1.Fence,
		Branch: "agentmesh/task-1-f1", PRURL: "https://github.com/o/r/pull/7", Summary: "all good"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Fail(ctx, "host-a", failed.ID, api.FailRequest{Fence: c2.Fence, Reason: "build broke", SessionID: "worker-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Fail(ctx, "host-a", retried.ID, api.FailRequest{Fence: c3.Fence, Retry: true, Reason: "flaky"}); err != nil {
		t.Fatal(err)
	}
	// Replay must not send a second notice.
	if again, err := e.st.Complete(ctx, "host-a", done.ID, api.CompleteRequest{Fence: c1.Fence}); err != nil || !again {
		t.Fatalf("replay %v %v", again, err)
	}
	// Reaper: attempt cap reached and lease expired -> stuck notice.
	if _, err := e.db.Exec("UPDATE tasks SET max_attempts=1 WHERE id=?", stuck.ID); err != nil {
		t.Fatal(err)
	}
	_ = c4
	e.clock.Advance(time.Hour)
	if r, err := e.st.Reap(ctx); err != nil || r.Stuck != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	msgs, err := e.st.Inbox(ctx, "host-a", "boss", true)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("%d messages: %+v %v", len(msgs), msgs, err)
	}
	for _, m := range msgs {
		if m.FromSession != "hub" || m.FromHost != "hub" {
			t.Fatalf("sender %+v", m)
		}
	}
	for _, want := range []string{fmt.Sprintf("task #%d done", done.ID), "branch: agentmesh/task-1-f1",
		"pr: https://github.com/o/r/pull/7", "result: all good"} {
		if !strings.Contains(msgs[0].Body, want) {
			t.Errorf("done notice missing %q: %q", want, msgs[0].Body)
		}
	}
	if !strings.Contains(msgs[1].Body, "stuck") || !strings.Contains(msgs[1].Body, "result: build broke") {
		t.Errorf("fail notice: %q", msgs[1].Body)
	}
	if !strings.Contains(msgs[2].Body, fmt.Sprintf("task #%d stuck", stuck.ID)) {
		t.Errorf("reaper notice: %q", msgs[2].Body)
	}
	if got := e.get(t, failed.ID); got.SessionID != "worker-1" {
		t.Errorf("fail session_id %q", got.SessionID)
	}
}

func TestFailKeepsSessionIDWhenOmitted(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	task := e.add(t, spec("r"))
	c := e.claim(t, "host-a")
	if _, err := e.st.Fail(ctx, "host-a", task.ID, api.FailRequest{Fence: c.Fence, Retry: true, SessionID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	c = e.claim(t, "host-a")
	if _, err := e.st.Fail(ctx, "host-a", task.ID, api.FailRequest{Fence: c.Fence}); err != nil {
		t.Fatal(err)
	}
	if got := e.get(t, task.ID); got.SessionID != "run-1" {
		t.Fatalf("session_id %q", got.SessionID)
	}
}

func TestAddTaskChecksCreatorSessionOwnership(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.session(t, "host-a", "s1", proj)
	s := spec("r")
	s.CreatedBySession = "s1"
	if _, err := e.st.AddTask(ctx, s, api.Principal{Host: "host-b", Session: true}); err != store.ErrSessionOwner {
		t.Fatalf("other host: %v", err)
	}
	if _, err := e.st.AddTask(ctx, s, api.Principal{Host: "host-a", Session: true}); err != nil {
		t.Fatal(err)
	}
	s.CreatedBySession = "ghost"
	if _, err := e.st.AddTask(ctx, s, api.Principal{Admin: true}); err != store.ErrSessionNotFound {
		t.Fatalf("ghost: %v", err)
	}
}
