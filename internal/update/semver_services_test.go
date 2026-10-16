package update

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/felixn678/agent-mesh/internal/onboard"
)

func TestCompareTags(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"v1.2.3", "v1.2.3", 0},
		{"v1.2.10", "v1.2.9", 1},
		{"v2.0.0", "v10.0.0", -1},
		{"v1.0.0", "v1.0.0-rc.1", 1},
		{"v1.0.0-rc.2", "v1.0.0-rc.10", -1},
		{"v1.0.0-alpha", "v1.0.0-alpha.1", -1},
		{"v1.0.0-1", "v1.0.0-alpha", -1},
	} {
		if got := compareTags(tc.a, tc.b); got != tc.want {
			t.Errorf("compareTags(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got := compareTags(tc.b, tc.a); got != -tc.want {
			t.Errorf("compareTags(%s, %s) = %d, want %d", tc.b, tc.a, got, -tc.want)
		}
	}
}

type recordingMgr struct {
	onboard.Manager
	restarted []string
}

func (m *recordingMgr) Restart(_ context.Context, name string) error {
	m.restarted = append(m.restarted, name)
	return nil
}

// The onboarding adapter restarts only services whose unit runs the updated
// binary, through the onboarding service manager.
func TestOnboardServicesFindsUnitsRunningThisBinary(t *testing.T) {
	home := t.TempDir()
	mgr := &recordingMgr{}
	e := &onboard.Env{Home: home, GOOS: "linux", Mgr: mgr}
	exe := "/home/user/.local/bin/agent-mesh"
	for name, bin := range map[string]string{"hub": exe, "daemon": "/opt/other/agent-mesh"} {
		p := onboard.UnitPath(e, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, p, []byte("[Service]\nExecStart="+bin+" "+name+"\n"))
	}
	s := NewServices(e)
	running, other := s.Installed(exe)
	if !slices.Equal(running, []string{"hub"}) || !slices.Equal(other, []string{"daemon"}) {
		t.Fatalf("running %v other %v", running, other)
	}
	if err := s.Restart(context.Background(), "hub"); err != nil || !slices.Equal(mgr.restarted, []string{"hub"}) {
		t.Fatalf("restart via manager: %v %v", err, mgr.restarted)
	}

	e.GOOS = "darwin"
	if running, other := s.Installed(exe); len(running)+len(other) != 0 {
		t.Fatalf("no launchd plists, got %v %v", running, other)
	}
}
