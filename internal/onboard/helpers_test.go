package onboard

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/felixn678/agent-mesh/internal/hub"
)

// fakeRunner answers LookPath/Output from tables instead of running programs.
type fakeRunner struct {
	mu    sync.Mutex
	bins  map[string]bool   // tools "installed"
	outs  map[string]string // "name arg arg" -> output; missing key = command fails
	paths map[string]string // optional explicit LookPath results
	calls []string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		bins: map[string]bool{"claude": true, "git": true, "gh": true, "gitleaks": true},
		outs: map[string]string{"claude auth status": "logged in", "gh auth status": "ok"},
	}
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if p, ok := f.paths[name]; ok {
		return p, nil
	}
	if f.bins[name] {
		return "/fake/bin/" + name, nil
	}
	return "", errors.New("not found")
}

func (f *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.mu.Unlock()
	if out, ok := f.outs[key]; ok {
		return []byte(out), nil
	}
	return []byte("fake: no such command"), errors.New("exit status 1")
}

// fakeMgr stands in for systemd/launchd. Installing the hub unit runs the
// real hub run loop in-process; Reload sends SIGHUP to this process, which
// the hub's reload watcher handles.
type fakeMgr struct {
	t        *testing.T
	e        *Env // unit files are written under e.Home, like the real managers
	running  bool // value Running reports
	mu       sync.Mutex
	installs []string
	restarts []string
	reloads  int
	started  bool // the in-process hub is running (an installed unit stays running)
}

func (m *fakeMgr) Install(ctx context.Context, u Unit) error {
	m.mu.Lock()
	m.installs = append(m.installs, u.Name)
	if p := UnitPath(m.e, u.Name); p != "" {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			_ = os.WriteFile(p, []byte(u.Desc), 0o644)
		}
	}
	skip := u.Name != "hub" || m.started
	m.started = m.started || u.Name == "hub"
	m.mu.Unlock()
	if skip {
		return nil
	}
	hctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- hub.Run(hctx, []string{"--config", filepath.Join(os.Getenv("AGENTMESH_CONFIG_DIR"), "hub.toml")})
	}()
	m.t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			m.t.Errorf("hub run: %v", err)
		}
	})
	return nil
}

func (m *fakeMgr) Restart(_ context.Context, name string) error {
	m.mu.Lock()
	m.restarts = append(m.restarts, name)
	m.mu.Unlock()
	return nil
}

func (m *fakeMgr) Reload(context.Context, string) error {
	m.mu.Lock()
	m.reloads++
	m.mu.Unlock()
	return syscall.Kill(os.Getpid(), syscall.SIGHUP)
}

func (m *fakeMgr) Running(context.Context, string) (bool, error) { return m.running, nil }

// newTestEnv builds an Env rooted in a temp home. It never reads or writes
// the real home, config directory, systemd or launchd.
func newTestEnv(t *testing.T, hostname string) (*Env, *bytes.Buffer, *fakeMgr) {
	t.Helper()
	home := t.TempDir()
	out := &bytes.Buffer{}
	mgr := &fakeMgr{t: t, running: true}
	e := &Env{
		Home: home, ConfigDir: filepath.Join(home, ".config", "agent-mesh"),
		DataDir: filepath.Join(home, ".local", "share", "agent-mesh"),
		Binary:  "/fake/bin/agent-mesh", GOOS: "linux", Out: out, Runner: newFakeRunner(), Mgr: mgr,
		Hostname: func() (string, error) { return hostname, nil },
	}
	mgr.e = e
	return e, out, mgr
}

// useHome points HOME/XDG at e so the in-process hub resolves "~" inside it.
func useHome(t *testing.T, e *Env) {
	t.Helper()
	t.Setenv("HOME", e.Home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(e.Home, ".config"))
	t.Setenv("AGENTMESH_CONFIG_DIR", e.ConfigDir) // macOS ignores XDG_CONFIG_HOME
	t.Setenv("AGENTMESH_URL", "")
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// joinLine extracts the "agent-mesh join <url> <bundle>" arguments from invite output.
// joinLine returns {hub-url, invite-code} from invite output.
func joinLine(t *testing.T, out string) []string {
	t.Helper()
	var code string
	for _, l := range strings.Split(out, "\n") {
		if c, ok := strings.CutPrefix(l, "paste this code when asked: "); ok {
			code = strings.TrimSpace(c)
		}
		if _, rest, ok := strings.Cut(l, "carries the hub address "); ok && code != "" {
			url, _, _ := strings.Cut(rest, ". ")
			return []string{url, code}
		}
	}
	t.Fatalf("no join line in output:\n%s", out)
	return nil
}
