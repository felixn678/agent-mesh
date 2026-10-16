package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// env bundles a Store over a real SQLite file with a controllable clock.
type env struct {
	st    *store.Store
	db    *sql.DB
	clock *fakeClock
	path  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := &fakeClock{t: time.UnixMilli(1_800_000_000_000)}
	return &env{st: store.New(db, clock.Now, store.DefaultLeaseConfig()), db: db, clock: clock, path: path}
}

func spec(repo string) api.TaskSpec {
	return api.TaskSpec{Repo: repo, Prompt: "p", MaxBudgetUSD: 5}
}

func (e *env) add(t *testing.T, s api.TaskSpec) api.Task {
	t.Helper()
	task, err := e.st.Add(context.Background(), s, "test")
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// anyCaps matches the default test tasks.
var anyCaps = api.ClaimRequest{
	Repos: []string{"r", "other"}, Modes: []string{"acceptEdits", "dontAsk", "plan"},
	Profiles: []string{"default", "docs"}, BudgetCap: 50,
}

func (e *env) claim(t *testing.T, host string) *api.ClaimedTask {
	t.Helper()
	c, err := e.st.Claim(context.Background(), host, anyCaps)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (e *env) get(t *testing.T, id int64) api.Task {
	t.Helper()
	task, err := e.st.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func wantErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("got error %v, want %v", got, want)
	}
}
