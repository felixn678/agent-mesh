package onboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// doctor must say when $AGENTMESH_URL sends CLI commands to a different hub
// than the one hooks, the waiter and MCP use (they ignore the variable).
func TestDoctorWarnsWhenEnvHubDiffersFromSessionHub(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "client.toml"), []byte("hub_url = \"http://127.0.0.1:8787\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := &Env{ConfigDir: dir}
	t.Setenv("AGENTMESH_URL", "http://127.0.0.1:9999")
	url, src := hubURLFor(e)
	r, ok := sessionURLCheck(e, url, src)
	if !ok || r.Status != Warn || !strings.Contains(r.Detail, "http://127.0.0.1:8787") || !strings.Contains(r.Detail, "9999") {
		t.Fatalf("want a session hub warning, got %+v %v", r, ok)
	}
	t.Setenv("AGENTMESH_URL", "http://127.0.0.1:8787")
	url, src = hubURLFor(e)
	if _, ok := sessionURLCheck(e, url, src); ok {
		t.Fatal("warned although both resolve to the same hub")
	}
	t.Setenv("AGENTMESH_URL", "")
	url, src = hubURLFor(e)
	if _, ok := sessionURLCheck(e, url, src); ok {
		t.Fatal("warned without $AGENTMESH_URL")
	}
}
