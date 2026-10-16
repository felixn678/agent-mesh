package hub

import (
	"testing"

	"github.com/felixn678/agent-mesh/internal/config"
)

func TestReloadSwapsTokenTables(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AdminTokenHash = config.HashToken("admin-1")
	a := newAuthenticator(cfg)
	if _, ok := a.identify("new-host"); ok {
		t.Fatal("unknown token accepted")
	}
	cfg.HostTokens = map[string]string{"box": config.HashToken("new-host")}
	a.replace(newAuthenticator(cfg))
	p, ok := a.identify("new-host")
	if !ok || p.Host != "box" {
		t.Fatalf("reloaded token rejected: %+v %v", p, ok)
	}
	cfg.HostTokens = nil
	a.replace(newAuthenticator(cfg))
	if _, ok := a.identify("new-host"); ok {
		t.Fatal("removed token still accepted")
	}
}
