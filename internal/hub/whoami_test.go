package hub_test

import (
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/hubtest"
)

func TestWhoAmI(t *testing.T) {
	h := hubtest.Start(t, "box")
	if code, _ := do(t, "GET", h.URL+"/api/v1/whoami", "", ""); code != 401 {
		t.Errorf("no token: %d", code)
	}
	for _, c := range []struct{ token, kind string }{
		{h.AdminToken, `"kind":"admin"`}, {h.SessionToken("box"), `"kind":"session"`}, {h.HostToken("box"), `"kind":"host"`},
	} {
		code, body := do(t, "GET", h.URL+"/api/v1/whoami", c.token, "")
		if code != 200 || !strings.Contains(body, c.kind) {
			t.Errorf("%s: %d %s", c.kind, code, body)
		}
	}
}
