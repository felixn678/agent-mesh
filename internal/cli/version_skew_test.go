package cli

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/hubtest"
	"github.com/felixn678/agent-mesh/internal/version"
)

// skewHub runs a hub whose responses claim hubVersion ("" = an older hub
// that sends no version header) while this binary claims local.
func skewHub(t *testing.T, local, hubVersion string) *hubtest.Hub {
	t.Helper()
	old := version.Version
	version.Version = local
	t.Cleanup(func() { version.Version = old }) // registered first: runs after the hub stops
	h := startHub(t)
	target, _ := url.Parse(h.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(r *http.Response) error {
		r.Header.Del(version.Header)
		if hubVersion != "" {
			r.Header.Set(version.Header, hubVersion)
		}
		return nil
	}
	front := httptest.NewServer(proxy)
	t.Cleanup(front.Close)
	testEnv(t, h)
	writeClientTOML(t, front.URL)
	return h
}

func TestSessionStartWarnsOnVersionSkew(t *testing.T) {
	h := skewHub(t, "v0.1.0", "v0.1.2")
	ho, _ := hook(t, "session-start", "hook_session_start.json")
	ctx := ho.HookSpecificOutput.AdditionalContext
	if want := "[agentmesh] this machine runs agent-mesh v0.1.0 but the hub runs v0.1.2 → run `agent-mesh update` here"; !strings.Contains(ctx, want) {
		t.Fatalf("no skew line in %q", ctx)
	}
	if strings.Count(ctx, "agent-mesh update") != 1 {
		t.Fatalf("skew line repeated: %q", ctx)
	}
	list, err := h.Store.ListSessions(bg, false)
	if err != nil || len(list) != 1 || list[0].AgentVersion != "v0.1.0" {
		t.Fatalf("stored version: %+v %v", list, err)
	}
}

func TestSessionStartQuietOnMatchingOrUnknownHubVersion(t *testing.T) {
	for _, c := range []struct{ name, local, hub string }{
		{"same", "v0.1.2", "v0.1.2"}, {"old hub without header", "v0.1.2", ""}, {"dev build", "dev", "v0.1.2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			skewHub(t, c.local, c.hub)
			ho, _ := hook(t, "session-start", "hook_session_start.json")
			if ctx := ho.HookSpecificOutput.AdditionalContext; ctx == "" || strings.Contains(ctx, "agent-mesh update") {
				t.Fatalf("context %q", ctx)
			}
		})
	}
}

func TestHostsMarksSkewedSessions(t *testing.T) {
	h := skewHub(t, "v0.1.2", "v0.1.2")
	b := h.Session("host-b")
	if _, err := b.RegisterSession(bg, api.RegisterSessionRequest{ID: "peer-0001", Cwd: "/w/api", AgentVersion: "v0.1.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RegisterSession(bg, api.RegisterSessionRequest{ID: "peer-0002", Cwd: "/w/web", AgentVersion: "v0.1.2"}); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "sessions")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "peer-000 host-b api seen") || strings.Count(out, "≠") != 1 ||
		!strings.Contains(out, "version v0.1.0 ≠ hub v0.1.2 → run `agent-mesh update` there") {
		t.Fatalf("sessions output:\n%s", out)
	}
}

func TestHostsWithOldHubShowsNoMark(t *testing.T) {
	h := skewHub(t, "v0.1.2", "")
	if _, err := h.Session("host-b").RegisterSession(bg, api.RegisterSessionRequest{ID: "peer-0001", AgentVersion: "v0.1.0"}); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "hosts", "--sessions")
	if err != nil || strings.Contains(out, "≠") || !strings.Contains(out, "sessions (1 live)") {
		t.Fatalf("%v\n%s", err, out)
	}
}
