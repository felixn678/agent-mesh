package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/felixn678/agent-mesh/internal/config"
)

func TestHubURLPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTMESH_URL", "")
	if u, src := config.HubURL(dir); u != config.DefaultHubURL || src != "default" {
		t.Fatalf("empty dir: %s %s", u, src)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("daemon.toml", "hub_url = \"http://daemon:1\"\npoll_interval = \"30s\"\n")
	if u, src := config.HubURL(dir); u != "http://daemon:1" || src != "daemon.toml" {
		t.Fatalf("daemon.toml: %s %s", u, src)
	}
	write("client.toml", "hub_url = \"http://client:2\"\n")
	if u, src := config.HubURL(dir); u != "http://client:2" || src != "client.toml" {
		t.Fatalf("client.toml: %s %s", u, src)
	}
	t.Setenv("AGENTMESH_URL", "http://env:3")
	if u, src := config.HubURL(dir); u != "http://env:3" || src != "$AGENTMESH_URL" {
		t.Fatalf("env: %s %s", u, src)
	}
}
