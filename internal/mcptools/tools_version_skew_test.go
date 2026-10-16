package mcptools_test

import (
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/version"
)

// The hub's own version is the reference: sessions and daemons reporting a
// different release are marked with the fix; unknown versions are not.
func TestHostsAndSessionsMarkVersionSkew(t *testing.T) {
	old := version.Version
	version.Version = "v0.1.2"
	t.Cleanup(func() { version.Version = old }) // registered first: runs after the hub stops
	h := startHub(t)
	for id, v := range map[string]string{"aaaa-1111": "v0.1.0", "bbbb-2222": "v0.1.2", "cccc-3333": ""} {
		if _, err := h.Store.RegisterSession(bg, "host-a", api.RegisterSessionRequest{ID: id, AgentVersion: v}); err != nil {
			t.Fatal(err)
		}
	}
	load := api.HostLoad{Cores: 4, RAMGB: 8, Slots: 1, AgentVersion: "v0.1.3"}
	if err := h.Store.UpsertHostLoad(bg, "host-b", load); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, h, api.Principal{Host: "host-a", Session: true})
	out := mustOK(t, cs, "sessions", struct{}{}, "aaaa-111 host-a - seen 0s ago, version v0.1.0 ≠ hub v0.1.2 → run `agent-mesh update` there")
	if strings.Count(out, "≠") != 1 {
		t.Fatalf("only the old session should be marked:\n%s", out)
	}
	mustOK(t, cs, "hosts", struct{}{}, "version v0.1.3 ≠ hub v0.1.2 → run `agent-mesh update` on the hub machine")
}
