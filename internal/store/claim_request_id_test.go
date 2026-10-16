package store_test

import (
	"sync"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
)

func capsWithID(id string) api.ClaimRequest {
	c := anyCaps
	c.RequestID = id
	return c
}

func TestClaimRetryWithSameRequestIDReturnsSameTask(t *testing.T) {
	e := newEnv(t)
	first := e.add(t, spec("r"))
	second := e.add(t, spec("r"))
	a, err := e.st.Claim(bg, "box", capsWithID("req-1"))
	if err != nil || a == nil || a.ID != first.ID {
		t.Fatalf("%+v %v", a, err)
	}
	// The response was lost; the daemon retries with the same id.
	b, err := e.st.Claim(bg, "box", capsWithID("req-1"))
	if err != nil || b == nil || *b != *a {
		t.Fatalf("retry got %+v want %+v (%v)", b, a, err)
	}
	if got := e.get(t, first.ID); got.Attempts != 1 || got.Fence != a.Fence {
		t.Fatalf("retry consumed an attempt: %+v", got)
	}
	// Same id from another host, or a new id, claims the next task.
	if c, err := e.st.Claim(bg, "other", capsWithID("req-1")); err != nil || c == nil || c.ID != second.ID {
		t.Fatalf("other host: %+v %v", c, err)
	}
	if c, _ := e.st.Claim(bg, "box", capsWithID("req-2")); c != nil {
		t.Fatalf("queue should be empty: %+v", c)
	}
	// After the task finished, the old id no longer replays.
	if _, err := e.st.Complete(bg, "box", a.ID, api.CompleteRequest{Fence: a.Fence}); err != nil {
		t.Fatal(err)
	}
	third := e.add(t, spec("r"))
	if c, err := e.st.Claim(bg, "box", capsWithID("req-1")); err != nil || c == nil || c.ID != third.ID {
		t.Fatalf("finished task replayed: %+v %v", c, err)
	}
}

func TestConcurrentRetriesOfOneClaimTakeOneTask(t *testing.T) {
	e := newEnv(t)
	for range 5 {
		e.add(t, spec("r"))
	}
	var wg sync.WaitGroup
	ids := make(chan int64, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, err := e.st.Claim(bg, "box", capsWithID("same")); err == nil && c != nil {
				ids <- c.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[int64]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("one request id claimed %d tasks", len(seen))
	}
}

func TestSessionClaimRetryWithSameRequestID(t *testing.T) {
	e := newEnv(t)
	e.session(t, "host-a", "s1", proj)
	task := e.add(t, api.TaskSpec{Repo: proj, Prompt: "p", MaxBudgetUSD: 1})
	e.add(t, api.TaskSpec{Repo: proj, Prompt: "p2", MaxBudgetUSD: 1})
	req := api.SessionClaimRequest{RequestID: "r-1"}
	a, err := e.st.ClaimForSessionRequest(bg, "host-a", "s1", req)
	if err != nil || a == nil || a.ID != task.ID {
		t.Fatalf("%+v %v", a, err)
	}
	b, err := e.st.ClaimForSessionRequest(bg, "host-a", "s1", req)
	if err != nil || b == nil || *b != *a {
		t.Fatalf("retry got %+v want %+v (%v)", b, a, err)
	}
	if _, err := e.st.ClaimForSessionRequest(bg, "host-a", "s1", api.SessionClaimRequest{RequestID: "bad id!"}); !api.IsValidation(err) {
		t.Fatalf("invalid request id: %v", err)
	}
}

func TestClaimRequestIDValidated(t *testing.T) {
	if err := capsWithID("has space").Validate(); !api.IsValidation(err) {
		t.Fatalf("got %v", err)
	}
}
