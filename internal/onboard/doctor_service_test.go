package onboard

import (
	"context"
	"strings"
	"testing"
)

// A hub started by hand (no service) but reachable is reported as such, as a
// warning; a hub that is neither a running service nor reachable stays "not
// running". Neither fails doctor.
func TestServiceChecksHubStartedManually(t *testing.T) {
	e := &Env{Mgr: &fakeMgr{t: t, running: false}}
	for _, tc := range []struct {
		reachable bool
		want      string
	}{
		{true, "hub reachable — started manually?"},
		{false, "not running"},
	} {
		rs := serviceChecks(context.Background(), e, true, tc.reachable)
		if len(rs) != 2 || rs[0].Name != "service hub" {
			t.Fatalf("results %+v", rs)
		}
		if rs[0].Status != Warn || !strings.Contains(rs[0].Detail, tc.want) {
			t.Errorf("reachable=%v: got %+v, want warn containing %q", tc.reachable, rs[0], tc.want)
		}
		if rs[1].Name != "service daemon" || rs[1].Detail != "not running" {
			t.Errorf("daemon result %+v", rs[1])
		}
	}
}
