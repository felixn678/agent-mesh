package hub_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/hubtest"
	"github.com/felixn678/agent-mesh/internal/version"
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

// Every API response carries the hub version header, and whoami reports it.
func TestHubReportsVersion(t *testing.T) {
	h := hubtest.Start(t, "box")
	req, _ := http.NewRequest("GET", h.URL+"/api/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+h.SessionToken("box"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var who api.WhoAmI
	if err := json.NewDecoder(resp.Body).Decode(&who); err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get(version.Header); got != version.Version || who.HubVersion != version.Version {
		t.Fatalf("header %q body %q, want %q", got, who.HubVersion, version.Version)
	}
	resp2, err := http.Get(h.URL + "/api/v1/hosts") // unauthenticated error responses carry it too
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 401 || resp2.Header.Get(version.Header) != version.Version {
		t.Fatalf("401: %d %q", resp2.StatusCode, resp2.Header.Get(version.Header))
	}
}
