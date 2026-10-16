package store_test

import (
	"path/filepath"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

func TestWorkerOptOutBlocksClaimAndClaimableCount(t *testing.T) {
	e := newEnv(t)
	off, on := false, true
	reg := func(w *bool) api.Session {
		t.Helper()
		s, err := e.st.RegisterSession(bg, "host-a", api.RegisterSessionRequest{ID: "s1", Repo: "github.com/o/r", Worker: w})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := reg(nil); !s.Worker {
		t.Fatal("new sessions default to worker")
	}
	e.add(t, spec("github.com/o/r"))
	if s := reg(&off); s.Worker {
		t.Fatal("worker=false not stored")
	}
	if s := reg(nil); s.Worker {
		t.Fatal("nil must keep the stored flag")
	}
	if n, _ := e.st.ClaimableCount(bg, "s1"); n != 0 {
		t.Fatalf("claimable for opted-out session: %d", n)
	}
	if _, err := e.st.ClaimForSession(bg, "host-a", "s1"); err != store.ErrNotWorker {
		t.Fatalf("claim: %v", err)
	}
	reg(&on)
	if c, err := e.st.ClaimForSession(bg, "host-a", "s1"); err != nil || c == nil {
		t.Fatalf("re-enabled claim: %+v %v", c, err)
	}
}

// A v5 database gains the worker column (default on) when reopened.
func TestOpenUpgradesFifthSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, host, repo, started_at, last_seen_at) VALUES ('old','host-a','github.com/o/r',1,1)`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"ALTER TABLE sessions DROP COLUMN worker", "PRAGMA user_version = 5"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = db.Close()
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	list, err := store.New(db, nil, store.DefaultLeaseConfig()).ListSessions(bg, false)
	if err != nil || len(list) != 1 || !list[0].Worker {
		t.Fatalf("%+v %v", list, err)
	}
}
