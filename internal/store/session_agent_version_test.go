package store_test

import (
	"path/filepath"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

// A registration without a version (an older hook, or a prompt heartbeat
// from one) keeps the version stored by the last one that sent it.
func TestRegisterSessionKeepsAgentVersion(t *testing.T) {
	st := newEnv(t).st
	if _, err := st.RegisterSession(bg, "host-a", api.RegisterSessionRequest{ID: "s1", AgentVersion: "v0.1.0"}); err != nil {
		t.Fatal(err)
	}
	s, err := st.RegisterSession(bg, "host-a", api.RegisterSessionRequest{ID: "s1"})
	if err != nil || s.AgentVersion != "v0.1.0" {
		t.Fatalf("empty kept: %+v %v", s, err)
	}
	s, err = st.RegisterSession(bg, "host-a", api.RegisterSessionRequest{ID: "s1", AgentVersion: "v0.1.2"})
	if err != nil || s.AgentVersion != "v0.1.2" {
		t.Fatalf("updated: %+v %v", s, err)
	}
	if _, err := st.RegisterSession(bg, "host-a", api.RegisterSessionRequest{ID: "s2", AgentVersion: string(make([]byte, 101))}); !api.IsValidation(err) {
		t.Fatalf("oversized version accepted: %v", err)
	}
}

// A v6 database gains the agent_version column (empty) when reopened.
func TestOpenUpgradesSixthSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, host, started_at, last_seen_at) VALUES ('old','host-a',1,1)`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"ALTER TABLE sessions DROP COLUMN agent_version", "PRAGMA user_version = 6"} {
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
	list, err := st.ListSessions(bg, false)
	if err != nil || len(list) != 1 || list[0].AgentVersion != "" {
		t.Fatalf("%+v %v", list, err)
	}
	s, err := st.RegisterSession(bg, "host-a", api.RegisterSessionRequest{ID: "old", AgentVersion: "v0.1.0"})
	if err != nil || s.AgentVersion != "v0.1.0" {
		t.Fatalf("after upgrade: %+v %v", s, err)
	}
}
