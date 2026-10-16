package cli

import (
	"bytes"
	"context"
	"github.com/felixn678/agent-mesh/internal/cli/hubenv"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/config"
	"github.com/felixn678/agent-mesh/internal/hubtest"
)

var bg = context.Background()

// testEnv points HOME, config and state dirs at a temp dir, the hub URL at h
// and writes h's host-a session token as session.token.
func testEnv(t *testing.T, h *hubtest.Hub) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENTMESH_CONFIG_DIR", filepath.Join(filepath.Join(home, ".config"), "agent-mesh")) // macOS ignores XDG_CONFIG_HOME
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTMESH_TOKEN", "")
	t.Setenv("AGENTMESH_SESSION_TOKEN", "")
	t.Setenv("AGENTMESH_HOOKS", "")
	t.Setenv("AGENTMESH_SESSION_NAME", "")
	t.Setenv("AGENTMESH_URL", "")
	if h != nil {
		writeClientTOML(t, h.URL)
		p, err := hubenv.File(hubenv.SessionTokenFile)
		if err != nil {
			t.Fatal(err)
		}
		if err := config.WriteToken(p, h.SessionToken("host-a")); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// writeClientTOML points the config at url, as `up`/`join` do.
func writeClientTOML(t *testing.T, url string) {
	t.Helper()
	p, err := hubenv.File("client.toml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("hub_url = \""+url+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// startHub starts a test hub and waits until it serves, so stopping it early
// cannot cancel its startup grace (which hubtest reports as a test error).
func startHub(t *testing.T) *hubtest.Hub {
	t.Helper()
	h := hubtest.Start(t)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		resp, err := http.Get(h.URL + "/healthz")
		if err == nil {
			resp.Body.Close()
			return h
		}
		if time.Now().After(deadline) {
			t.Fatalf("hub not serving: %v", err)
		}
	}
}

// syncBuf is a goroutine-safe output buffer.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// runCLI runs args in-process with stdin and returns stdout, stderr.
func runCLI(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut syncBuf
	err := run(bg, cmdIO{in: strings.NewReader(stdin), out: &out, err: &errOut}, args)
	return out.String(), errOut.String(), err
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
