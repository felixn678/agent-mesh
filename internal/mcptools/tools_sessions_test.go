package mcptools_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/hubtest"
)

func register(t *testing.T, h *hubtest.Hub, host, id, name string) {
	t.Helper()
	if _, err := h.Store.RegisterSession(bg, host, api.RegisterSessionRequest{ID: id, Cwd: "/w/" + host, Name: name}); err != nil {
		t.Fatal(err)
	}
}

func TestSendResolvesTargetsAndInboxConsumes(t *testing.T) {
	h := startHub(t)
	register(t, h, "host-a", "aaaa-1111", "alpha")
	register(t, h, "host-b", "bbbb-2222", "beta")
	register(t, h, "host-b", "bbbb-3333", "")
	a := connect(t, h, api.Principal{Host: "host-a", Session: true})
	b := connect(t, h, api.Principal{Host: "host-b", Session: true})

	mustOK(t, a, "sessions", struct{}{}, "bbbb-222 host-b host-b [beta] seen")
	mustOK(t, a, "send", map[string]any{"from": "aaaa-1111", "to": "bbbb-2222", "text": "by id"}, "sent message 1 to bbbb-222")
	mustOK(t, a, "send", map[string]any{"from": "aaaa-1111", "to": "bbbb-3", "text": "by prefix"}, "to bbbb-333")
	mustOK(t, a, "send", map[string]any{"from": "aaaa-1111", "to": "beta", "text": "by name"}, "to bbbb-222")
	mustErr(t, a, "send", map[string]any{"from": "aaaa-1111", "to": "bbbb", "text": "x"}, "ambiguous; candidates:\nbbbb-")
	mustErr(t, a, "send", map[string]any{"from": "aaaa-1111", "to": "bbb", "text": "x"}, "no live session matches")
	mustErr(t, a, "send", map[string]any{"from": "bbbb-2222", "to": "aaaa-1111", "text": "spoof"}, "another host")

	out := mustOK(t, b, "inbox", map[string]any{"session": "bbbb-2222"}, "[agentmesh] 2 message(s)")
	for _, want := range []string{"from host-a/host-a [alpha] session aaaa-111", "> by id", "> by name"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	mustOK(t, b, "inbox", map[string]any{"session": "bbbb-2222"}, "no unread messages")
	mustErr(t, a, "inbox", map[string]any{"session": "bbbb-3333"}, "another host")
}

// TestMCPOverHTTPAuth exercises the real hub mount: 401 without a token, 403
// for daemon tokens, principal-scoped tool lists for session and admin.
func TestMCPOverHTTPAuth(t *testing.T) {
	h := startHub(t)
	resp, err := http.Post(h.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	list := func(token string) (string, error) {
		hc := &http.Client{Transport: bearerRT{token: token}}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(bg,
			&mcp.StreamableClientTransport{Endpoint: h.URL + "/mcp", HTTPClient: hc, MaxRetries: -1}, nil)
		if err != nil {
			return "", err
		}
		defer cs.Close()
		return strings.Join(toolNames(t, cs), ","), nil
	}
	if _, err := list(h.HostToken("host-a")); err == nil {
		t.Fatal("daemon token must not reach MCP")
	}
	sess, err := list(h.SessionToken("host-a"))
	if err != nil || strings.Contains(sess, "task_cancel") || !strings.Contains(sess, "send") {
		t.Fatalf("session: %s %v", sess, err)
	}
	admin, err := list(h.AdminToken)
	if err != nil || !strings.Contains(admin, "task_cancel") {
		t.Fatalf("admin: %s %v", admin, err)
	}
}

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
