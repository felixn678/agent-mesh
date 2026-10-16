package store_test

import (
	"context"
	"path/filepath"
	"strings"
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

// A second-version database gains the claim retry column.
func TestOpenUpgradesSecondSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"ALTER TABLE tasks DROP COLUMN claim_request_id", "PRAGMA user_version = 2"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = db.Close()
	if db, err = store.Open(path); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db, nil, store.DefaultLeaseConfig())
	ctx := context.Background()
	if _, err := st.Add(ctx, api.TaskSpec{Repo: "r", Prompt: "p", MaxBudgetUSD: 1}, "x"); err != nil {
		t.Fatal(err)
	}
	caps := api.ClaimRequest{Repos: []string{"r"}, Modes: []string{"acceptEdits"}, Profiles: []string{"default"},
		BudgetCap: 5, RequestID: "req"}
	a, err := st.Claim(ctx, "box", caps)
	if err != nil || a == nil {
		t.Fatalf("%+v %v", a, err)
	}
	if b, err := st.Claim(ctx, "box", caps); err != nil || b == nil || b.ID != a.ID {
		t.Fatalf("replay after upgrade: %+v %v", b, err)
	}
}

// A third-version database replaces the completion trigger, so a late Fail of
// a reaper-stuck task notifies the creator again.
func TestOpenUpgradesThirdSchemaVersionTrigger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"DROP TRIGGER tasks_notify_creator", v3Trigger, "PRAGMA user_version = 3"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = db.Close()
	if db, err = store.Open(path); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sqlText string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='trigger' AND name='tasks_notify_creator'").Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sqlText, "OLD.lease_expires_at IS NOT NULL") {
		t.Fatalf("trigger not upgraded: %s", sqlText)
	}
}

// v3Trigger is the completion trigger as shipped in schema version 3.
const v3Trigger = `CREATE TRIGGER tasks_notify_creator AFTER UPDATE OF state ON tasks
WHEN NEW.created_by_session IS NOT NULL AND NEW.state IN ('done','stuck') AND OLD.state <> NEW.state
BEGIN
  INSERT INTO messages (from_session, from_host, to_session, body, created_at)
  VALUES ('hub', 'hub', NEW.created_by_session, '[agent-mesh] task #' || NEW.id || ' ' || NEW.state, NEW.updated_at);
END`

// A fourth-version database gains the task-add request id and its index.
func TestOpenUpgradesFourthSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"DROP INDEX tasks_request_id", "ALTER TABLE tasks DROP COLUMN request_id", "PRAGMA user_version = 4"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = db.Close()
	if db, err = store.Open(path); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db, nil, store.DefaultLeaseConfig())
	spec := api.TaskSpec{Repo: "r", Prompt: "p", MaxBudgetUSD: 1, RequestID: "once"}
	a, _, err := st.AddTaskReplay(context.Background(), spec, api.Principal{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	b, replayed, err := st.AddTaskReplay(context.Background(), spec, api.Principal{Admin: true})
	if err != nil || !replayed || b.ID != a.ID {
		t.Fatalf("replay after upgrade: %+v %v %v", b, replayed, err)
	}
}
