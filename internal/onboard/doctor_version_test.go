package onboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/version"
)

func TestDoctorVersionCheck(t *testing.T) {
	old := version.Version
	t.Cleanup(func() { version.Version = old })
	for _, c := range []struct {
		name, local, body string
		status            Status
		detail            string
	}{
		{"older here", "v0.1.0", `{"kind":"session","hub_version":"v0.1.2"}`, Warn, "run `agent-mesh update` here"},
		{"newer here", "v0.1.3", `{"kind":"session","hub_version":"v0.1.2"}`, Warn, "run `agent-mesh update` on the hub machine"},
		{"same", "v0.1.2", `{"kind":"session","hub_version":"v0.1.2"}`, Pass, "same as the hub"},
		{"old hub", "v0.1.2", `{"kind":"session"}`, Pass, "hub unreported; not compared"},
		{"dev build", "dev", `{"kind":"session","hub_version":"v0.1.2"}`, Pass, "not compared"},
	} {
		t.Run(c.name, func(t *testing.T) {
			version.Version = c.local
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			r, ok := versionCheck(context.Background(), srv.URL, "tok")
			if !ok || r.Status != c.status || !strings.Contains(r.Detail, c.detail) {
				t.Fatalf("%+v %v", r, ok)
			}
		})
	}
	if _, ok := versionCheck(context.Background(), "http://127.0.0.1:1", "tok"); ok {
		t.Fatal("unreachable hub produced a version line")
	}
}
