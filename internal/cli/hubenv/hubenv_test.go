package hubenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/felixn678/agent-mesh/internal/config"
)

func setup(t *testing.T) string {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	for _, k := range []string{"AGENTMESH_URL", "AGENTMESH_TOKEN", "AGENTMESH_SESSION_TOKEN"} {
		t.Setenv(k, "")
	}
	dir := filepath.Join(cfg, "agent-mesh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestHubURLOrder(t *testing.T) {
	dir := setup(t)
	if HubURL() != DefaultHubURL {
		t.Fatal(HubURL())
	}
	// daemon.toml with keys we do not know still yields hub_url.
	os.WriteFile(filepath.Join(dir, "daemon.toml"), []byte("hub_url = \"http://d:1\"\nmax_slots = 2\n[repos]\nx = \"y\"\n"), 0o600)
	if HubURL() != "http://d:1" {
		t.Fatal(HubURL())
	}
	os.WriteFile(filepath.Join(dir, "client.toml"), []byte("hub_url = \"http://c:1\"\n"), 0o600)
	if HubURL() != "http://c:1" {
		t.Fatal(HubURL())
	}
	t.Setenv("AGENTMESH_URL", "http://e:1")
	if HubURL() != "http://e:1" {
		t.Fatal(HubURL())
	}
}

func tok(t *testing.T, f func() (string, error)) string {
	t.Helper()
	s, err := f()
	if err != nil {
		return "err"
	}
	return s
}

func TestTokenPrecedence(t *testing.T) {
	dir := setup(t)
	for _, f := range []func() (string, error){SessionToken, ClientToken, AdminToken} {
		if tok(t, f) != "err" {
			t.Fatal("no token files must error")
		}
	}
	config.WriteToken(filepath.Join(dir, SessionTokenFile), "sess")
	if tok(t, SessionToken) != "sess" || tok(t, ClientToken) != "sess" || tok(t, AdminToken) != "sess" {
		t.Fatal("session token fallback")
	}
	config.WriteToken(filepath.Join(dir, AdminTokenFile), "adm")
	if tok(t, ClientToken) != "sess" || tok(t, AdminToken) != "adm" {
		t.Fatal("admin file preference")
	}
	t.Setenv("AGENTMESH_TOKEN", "env")
	if tok(t, ClientToken) != "env" || tok(t, AdminToken) != "env" || tok(t, SessionToken) != "sess" {
		t.Fatal("AGENTMESH_TOKEN must not reach session-only callers")
	}
	t.Setenv("AGENTMESH_SESSION_TOKEN", "envsess")
	if tok(t, SessionToken) != "envsess" {
		t.Fatal("AGENTMESH_SESSION_TOKEN")
	}
	// Group-readable token files are refused.
	os.Chmod(filepath.Join(dir, AdminTokenFile), 0o644)
	t.Setenv("AGENTMESH_TOKEN", "")
	if tok(t, AdminToken) != "err" {
		t.Fatal("world-readable admin.token accepted")
	}
}
