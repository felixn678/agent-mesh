package store_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

func TestHostProfilesMergeNotesToolsAndLiveSessions(t *testing.T) {
	e := newEnv(t)
	mac := &api.MachineInfo{OS: "macos/arm64", Tools: []string{"xcode", "adb"}}
	if _, err := e.st.RegisterSession(bg, "mac", api.RegisterSessionRequest{ID: "m1", Machine: mac}); err != nil {
		t.Fatal(err)
	}
	reg(t, e, "mac", "m2")                                                         // heartbeat without machine info keeps the tools
	reg(t, e, "ubuntu", "u1")                                                      // live session, no profile row yet
	if err := e.st.UpsertHostLoad(bg, "box", api.HostLoad{Cores: 4}); err != nil { // daemon only
		t.Fatal(err)
	}
	if err := e.st.SetHostNote(bg, "mac", "mac", api.HostNoteRequest{Note: "  frontend, Expo  "}); err != nil {
		t.Fatal(err)
	}
	wantErr(t, e.st.SetHostNote(bg, "mac", "ubuntu", api.HostNoteRequest{Note: "x"}), store.ErrHostNoteOwner)
	if err := e.st.SetHostNote(bg, "", "ubuntu", api.HostNoteRequest{Note: "backend + web"}); err != nil { // admin
		t.Fatal(err)
	}
	list, err := e.st.ListHostProfiles(bg)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]api.HostProfile{}
	for _, h := range list {
		got[h.Name] = h
	}
	if len(list) != 3 || list[0].Name != "box" {
		t.Fatalf("names: %+v", list)
	}
	if m := got["mac"]; m.Note != "frontend, Expo" || m.OS != "macos/arm64" || strings.Join(m.Tools, ",") != "xcode,adb" || m.LiveSessions != 2 {
		t.Fatalf("mac: %+v", m)
	}
	if u := got["ubuntu"]; u.Note != "backend + web" || u.LiveSessions != 1 || u.Tools != nil {
		t.Fatalf("ubuntu: %+v", u)
	}
	if b := got["box"]; b.Note != "" || b.LiveSessions != 0 {
		t.Fatalf("box: %+v", b)
	}
	// A new machine report keeps the note; clearing the note keeps the tools.
	if _, err := e.st.RegisterSession(bg, "mac", api.RegisterSessionRequest{ID: "m1", Machine: &api.MachineInfo{OS: "macos/arm64", Tools: []string{"node"}}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetHostNote(bg, "mac", "mac", api.HostNoteRequest{}); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(13 * time.Hour) // every session goes stale
	list, _ = e.st.ListHostProfiles(bg)
	for _, h := range list {
		if h.LiveSessions != 0 {
			t.Fatalf("stale sessions counted: %+v", h)
		}
		if h.Name == "mac" && (h.Note != "" || strings.Join(h.Tools, ",") != "node") {
			t.Fatalf("mac after updates: %+v", h)
		}
	}
}

func TestHostProfileValidation(t *testing.T) {
	e := newEnv(t)
	for _, err := range []error{
		e.st.SetHostNote(bg, "", "bad name", api.HostNoteRequest{Note: "x"}),
		e.st.SetHostNote(bg, "", "mac", api.HostNoteRequest{Note: strings.Repeat("x", api.MaxHostNoteBytes+1)}),
		func() error {
			_, err := e.st.RegisterSession(bg, "mac", api.RegisterSessionRequest{ID: "m1", Machine: &api.MachineInfo{Tools: []string{"Bad Tool"}}})
			return err
		}(),
	} {
		if !api.IsValidation(err) {
			t.Fatalf("want validation error, got %v", err)
		}
	}
}

// A v7 database gains the host_profiles table when reopened.
func TestOpenUpgradesSeventhSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"DROP TABLE host_profiles", "PRAGMA user_version = 7"} {
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
	if err := st.SetHostNote(bg, "", "mac", api.HostNoteRequest{Note: "frontend"}); err != nil {
		t.Fatalf("after upgrade: %v", err)
	}
}
