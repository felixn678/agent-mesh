package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

// A database written by the first schema version gains the session/targeting
// columns and the completion trigger when reopened.
func TestOpenUpgradesFirstSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"DROP TRIGGER tasks_notify_creator",
		"ALTER TABLE tasks DROP COLUMN target_host", "ALTER TABLE tasks DROP COLUMN target_session",
		"ALTER TABLE tasks DROP COLUMN created_by_session", "ALTER TABLE tasks DROP COLUMN summary",
		"ALTER TABLE sessions DROP COLUMN repo", "PRAGMA user_version = 1",
	} {
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
	st := store.New(db, nil, store.DefaultLeaseConfig())
	ctx := context.Background()
	if _, err := st.RegisterSession(ctx, "host-a", api.RegisterSessionRequest{ID: "s1", Repo: "github.com/o/r"}); err != nil {
		t.Fatal(err)
	}
	spec := api.TaskSpec{Repo: "github.com/o/r", Prompt: "p", MaxBudgetUSD: 1, TargetSession: "s1", CreatedBySession: "s1"}
	if _, err := st.Add(ctx, spec, "x"); err != nil {
		t.Fatal(err)
	}
	if c, err := st.ClaimForSession(ctx, "host-a", "s1"); err != nil || c == nil {
		t.Fatalf("%+v %v", c, err)
	}
}
