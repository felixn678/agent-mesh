package hub_test

import (
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/hubtest"
)

// A session token sets only its own machine's note; admin sets any; daemons
// cannot write notes; every token can read the profiles.
func TestHostNoteAuthorization(t *testing.T) {
	h := hubtest.Start(t, "mac", "ubuntu")
	note := func(name, token string) int {
		code, _ := do(t, "PUT", h.URL+"/api/v1/hosts/"+name+"/note", token, `{"note":"frontend"}`)
		return code
	}
	for _, c := range []struct {
		name, token string
		want        int
	}{
		{"mac", h.SessionToken("mac"), 204},
		{"ubuntu", h.SessionToken("mac"), 403},
		{"ubuntu", h.AdminToken, 204},
		{"mac", h.HostToken("mac"), 403},
		{"mac", "", 401},
		{"bad%20name", h.AdminToken, 400},
	} {
		if got := note(c.name, c.token); got != c.want {
			t.Errorf("%s with %q: %d, want %d", c.name, c.token, got, c.want)
		}
	}
	if code, _ := do(t, "PUT", h.URL+"/api/v1/hosts/mac/note", h.AdminToken, `{"note":"`+strings.Repeat("x", 301)+`"}`); code != 400 {
		t.Errorf("long note: %d", code)
	}
	for _, tok := range []string{h.AdminToken, h.SessionToken("ubuntu"), h.HostToken("mac")} {
		code, body := do(t, "GET", h.URL+"/api/v1/hosts/profiles", tok, "")
		if code != 200 || !strings.Contains(body, `"name":"mac","note":"frontend"`) {
			t.Fatalf("profiles: %d %s", code, body)
		}
	}
}
