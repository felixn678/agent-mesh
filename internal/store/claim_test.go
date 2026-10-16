package store_test

import (
	"context"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
)

func TestClaimEmptyQueue(t *testing.T) {
	e := newEnv(t)
	if c := e.claim(t, "box"); c != nil {
		t.Fatalf("expected nil, got %+v", c)
	}
}

func TestClaimFIFOAndFenceStart(t *testing.T) {
	e := newEnv(t)
	a, b := e.add(t, spec("r")), e.add(t, spec("r"))
	c1, c2 := e.claim(t, "box"), e.claim(t, "box")
	if c1.ID != a.ID || c2.ID != b.ID {
		t.Fatalf("not FIFO: %d,%d want %d,%d", c1.ID, c2.ID, a.ID, b.ID)
	}
	if c1.Fence != 1 {
		t.Fatalf("first fence = %d", c1.Fence)
	}
	got := e.get(t, a.ID)
	if got.State != api.StateRunning || got.ClaimedBy != "box" || got.Attempts != 1 || got.LeaseExpiresAt == 0 {
		t.Fatalf("unexpected row: %+v", got)
	}
	if e.claim(t, "box") != nil {
		t.Fatal("running tasks must not be claimable")
	}
}

func TestClaimHostConstraint(t *testing.T) {
	e := newEnv(t)
	s := spec("r")
	s.Host = "big"
	e.add(t, s)
	if e.claim(t, "small") != nil {
		t.Fatal("wrong host claimed a pinned task")
	}
	if e.claim(t, "big") == nil {
		t.Fatal("pinned host could not claim")
	}
}

func TestClaimCapsFilter(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := spec("r")
	s.PermissionMode, s.ToolProfile, s.MaxBudgetUSD = "plan", "docs", 20
	e.add(t, s)

	miss := map[string]api.ClaimRequest{
		"repo":    {Repos: []string{"x"}, Modes: []string{"plan"}, Profiles: []string{"docs"}, BudgetCap: 50},
		"mode":    {Repos: []string{"r"}, Modes: []string{"dontAsk"}, Profiles: []string{"docs"}, BudgetCap: 50},
		"profile": {Repos: []string{"r"}, Modes: []string{"plan"}, Profiles: []string{"default"}, BudgetCap: 50},
		"budget":  {Repos: []string{"r"}, Modes: []string{"plan"}, Profiles: []string{"docs"}, BudgetCap: 19.99},
		"nil":     {},
	}
	for name, req := range miss {
		c, err := e.st.Claim(ctx, "box", req)
		if err != nil || c != nil {
			t.Fatalf("%s: claimed %+v err=%v", name, c, err)
		}
	}
	hit := api.ClaimRequest{Repos: []string{"r"}, Modes: []string{"plan"}, Profiles: []string{"docs"}, BudgetCap: 20}
	if c, err := e.st.Claim(ctx, "box", hit); err != nil || c == nil {
		t.Fatalf("matching caps did not claim: %v %v", c, err)
	}
}

func TestClaimSkipsIneligibleHeadOfQueue(t *testing.T) {
	e := newEnv(t)
	first := spec("other-repo")
	e.add(t, first) // head of queue, host lacks this repo
	second := e.add(t, spec("r"))
	req := api.ClaimRequest{Repos: []string{"r"}, Modes: []string{"acceptEdits"}, Profiles: []string{"default"}, BudgetCap: 10}
	c, err := e.st.Claim(context.Background(), "box", req)
	if err != nil || c == nil || c.ID != second.ID {
		t.Fatalf("poison head vetoed the queue: %+v %v", c, err)
	}
}
