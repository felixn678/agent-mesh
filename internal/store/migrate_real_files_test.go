package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

// testdata/schema-v{1,3,5}.db were written by the store code of the commits
// that shipped those schema versions (same data each time: one done, one
// stuck, one running and one queued task, two sessions, one unread message).
// Opening them with today's code must upgrade in place, keep every row, and
// leave every current feature working.
func TestUpgradeRealOldDatabaseFiles(t *testing.T) {
	for _, name := range []string{"schema-v1.db", "schema-v3.db", "schema-v5.db"} {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "hub.db")
			if err := os.WriteFile(path, src, 0o600); err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(path)
			if err != nil {
				t.Fatalf("open old file: %v", err)
			}
			defer db.Close()
			var ver int
			var integrity string
			_ = db.QueryRow("PRAGMA user_version").Scan(&ver)
			_ = db.QueryRow("PRAGMA integrity_check").Scan(&integrity)
			if integrity != "ok" {
				t.Fatalf("integrity: %s", integrity)
			}
			st := store.New(db, nil, store.DefaultLeaseConfig())
			checkOldRows(t, st)
			checkNewFeatures(t, st)
			// Reopening an upgraded file is a no-op.
			db2, err := store.Open(path)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			_ = db2.Close()
			var after int
			_ = db.QueryRow("PRAGMA user_version").Scan(&after)
			if ver != after || ver < 6 {
				t.Fatalf("schema version %d then %d", ver, after)
			}
		})
	}
}

func checkOldRows(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	want := map[int64]string{1: api.StateDone, 2: api.StateStuck, 3: api.StateRunning, 4: api.StateQueued}
	for id, state := range want {
		got, err := st.Get(ctx, id)
		if err != nil || got.State != state {
			t.Fatalf("task %d: %+v %v (want %s)", id, got, err, state)
		}
	}
	done, _ := st.Get(ctx, 1)
	stuck, _ := st.Get(ctx, 2)
	if done.PRURL != "https://github.com/example/r/pull/1" || done.CostUSD != 0.5 || stuck.Reason != "boom" || stuck.Worktree != "/wt/2" {
		t.Fatalf("old fields lost: %+v / %+v", done, stuck)
	}
	msgs, err := st.Inbox(ctx, "box", "s2", false)
	if err != nil || len(msgs) != 1 || msgs[0].Body != "hello from the past" {
		t.Fatalf("old unread message: %+v %v", msgs, err)
	}
}

func checkNewFeatures(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	// Idempotent task add (v5) and a creator session (v2) with its notice (v2/v4 trigger).
	if _, err := st.RegisterSession(ctx, "box", api.RegisterSessionRequest{ID: "s1", Repo: "github.com/o/r"}); err != nil {
		t.Fatal(err)
	}
	spec := api.TaskSpec{Repo: "github.com/o/r", Prompt: "new", MaxBudgetUSD: 1, RequestID: "rid", CreatedBySession: "s1", TargetSession: "s1"}
	a, _, err := st.AddTaskReplay(ctx, spec, api.Principal{Host: "box", Session: true})
	if err != nil {
		t.Fatal(err)
	}
	if b, replayed, err := st.AddTaskReplay(ctx, spec, api.Principal{Host: "box", Session: true}); err != nil || !replayed || b.ID != a.ID {
		t.Fatalf("replay: %+v %v %v", b, replayed, err)
	}
	// Session claim with request id (v3), then done with summary -> notice.
	c, err := st.ClaimForSessionRequest(ctx, "box", "s1", api.SessionClaimRequest{RequestID: "c1"})
	if err != nil || c == nil || c.ID != a.ID {
		t.Fatalf("session claim: %+v %v", c, err)
	}
	if _, err := st.Complete(ctx, store.SessionHolderPrefix+"s1", c.ID, api.CompleteRequest{Fence: c.Fence, Summary: "ok"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.Inbox(ctx, "box", "s1", false)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("done notice: %+v %v", msgs, err)
	}
	if _, err := st.Reap(ctx); err != nil {
		t.Fatalf("reap on upgraded file: %v", err)
	}
}
