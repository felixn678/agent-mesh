package report_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/config"
	"github.com/felixn678/agent-mesh/internal/hubtest"
	"github.com/felixn678/agent-mesh/internal/report"
)

// hubWithQueuedTask points the CLI env at a test hub holding one queued task;
// queued tasks are always reported, so the real wall clock does not matter.
func hubWithQueuedTask(t *testing.T) *hubtest.Hub {
	h := hubtest.Start(t)
	fixture{t: t, h: h}.add("web", 0)
	t.Setenv("AGENTMESH_URL", h.URL)
	t.Setenv("AGENTMESH_TOKEN", h.AdminToken)
	return h
}

func TestRunPrintsReport(t *testing.T) {
	hubWithQueuedTask(t)
	var out strings.Builder
	if err := report.RunTo(bg, []string{"--since", "2h"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "## Queued (1)") {
		t.Fatalf("stdout:\n%s", out.String())
	}
}

func TestRunReadsAdminTokenFile(t *testing.T) {
	h := hubWithQueuedTask(t)
	t.Setenv("AGENTMESH_TOKEN", "")
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	if err := config.WriteToken(filepath.Join(cfgHome, "agent-mesh", "admin.token"), h.AdminToken); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := report.RunTo(bg, nil, &out); err != nil || !strings.Contains(out.String(), "# Morning report") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

func TestOutRefusesGitWorkTree(t *testing.T) {
	hubWithQueuedTask(t)
	repo := t.TempDir()
	if b, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, b)
	}
	target := filepath.Join(repo, "notes", "report.md") // parent does not exist yet
	err := report.RunTo(bg, []string{"--out", target}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "git work tree") {
		t.Fatalf("want refusal, got %v", err)
	}
	if _, err := os.Stat(filepath.Dir(target)); !os.IsNotExist(err) {
		t.Fatalf("refused write still created %s", filepath.Dir(target))
	}
}

func TestOutWritesPrivateFile(t *testing.T) {
	hubWithQueuedTask(t)
	dir := filepath.Join(t.TempDir(), "reports")
	target := filepath.Join(dir, "today.md")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil { // existing loose mode is tightened
		t.Fatal(err)
	}
	if err := report.RunTo(bg, []string{"--out", target}, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(target)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi.Mode().Perm(), err)
	}
	if b, _ := os.ReadFile(target); !strings.Contains(string(b), "## Queued (1)") {
		t.Fatalf("content:\n%s", b)
	}

	fresh := filepath.Join(t.TempDir(), "a", "b", "r.md")
	if err := report.RunTo(bg, []string{"--out", fresh}, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Dir(fresh)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode %v %v", fi.Mode().Perm(), err)
	}
}

// On a joined machine there is no admin token and the hub URL lives in
// client.toml; the report must still work from the session token.
func TestRunOnJoinedMachineUsesClientTOMLAndSessionToken(t *testing.T) {
	h := hubWithQueuedTask(t)
	t.Setenv("AGENTMESH_URL", "")
	t.Setenv("AGENTMESH_TOKEN", "")
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	dir := filepath.Join(cfgHome, "agent-mesh")
	if err := config.WriteToken(filepath.Join(dir, "session.token"), h.SessionToken("host-b")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "client.toml"), []byte("hub_url = \""+h.URL+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := report.RunTo(bg, nil, &out); err != nil || !strings.Contains(out.String(), "## Queued (1)") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}
