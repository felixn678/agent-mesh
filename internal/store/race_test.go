package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

// 16 claimers on 4 separate database handles (separate connections to one
// file) must each task exactly once, with unique (id, fence) pairs.
func TestClaimExactlyOnceUnderContention(t *testing.T) {
	const tasks, claimers, handles = 200, 16, 4
	path := filepath.Join(t.TempDir(), "hub.db")
	var stores []*store.Store
	for i := 0; i < handles; i++ {
		db, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		stores = append(stores, store.New(db, nil, store.DefaultLeaseConfig()))
	}
	ctx := context.Background()
	for i := 0; i < tasks; i++ {
		if _, err := stores[0].Add(ctx, spec("r"), "race"); err != nil {
			t.Fatal(err)
		}
	}

	type claim struct{ id, fence int64 }
	var mu sync.Mutex
	seen := map[claim]string{}
	perTask := map[int64]int{}
	var wg sync.WaitGroup
	errs := make(chan error, claimers)
	for w := 0; w < claimers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			st, host := stores[w%handles], fmt.Sprintf("host-%d", w)
			for {
				c, err := st.Claim(ctx, host, api.ClaimRequest{Repos: []string{"r"},
					Modes: []string{"acceptEdits"}, Profiles: []string{"default"}, BudgetCap: 10})
				if err != nil {
					errs <- fmt.Errorf("claimer %d: %w", w, err)
					return
				}
				if c == nil {
					return
				}
				mu.Lock()
				seen[claim{c.ID, c.Fence}] = host
				perTask[c.ID]++
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if len(perTask) != tasks {
		t.Fatalf("claimed %d distinct tasks, want %d", len(perTask), tasks)
	}
	for id, n := range perTask {
		if n != 1 {
			t.Fatalf("task %d claimed %d times", id, n)
		}
	}
	if len(seen) != tasks {
		t.Fatalf("%d unique (id,fence), want %d", len(seen), tasks)
	}
}
