package e2e_test

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/config"
)

// buildBinary compiles the real agent-mesh binary into a temp dir.
func buildBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("e2e uses POSIX shells and signals")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	bin := filepath.Join(t.TempDir(), "agent-mesh")
	cmd := exec.Command(goBin, "build", "-o", bin, "github.com/felixn678/agent-mesh/cmd/agent-mesh")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// fakeTools is a PATH holding stand-ins for the CLIs doctor probes, so no
// real claude/gh/systemctl/tailscale (or the user's real config) is touched.
func fakeTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"claude", "gh", "git", "gitleaks"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// machine is one fake user account: its own HOME and config dir. Commands run
// with a minimal environment, never inheriting the caller's HOME or AGENTMESH_*.
type machine struct {
	t   *testing.T
	bin string
	env []string
	cfg string // config dir
}

func newMachine(t *testing.T, bin, path string) *machine {
	home := t.TempDir()
	cfg := filepath.Join(home, ".config")
	return &machine{t: t, bin: bin, cfg: filepath.Join(cfg, "agent-mesh"),
		env: []string{"HOME=" + home, "XDG_CONFIG_HOME=" + cfg, "AGENTMESH_CONFIG_DIR=" + filepath.Join(cfg, "agent-mesh"), "PATH=" + path, "TMPDIR=" + os.TempDir()}}
}

func (m *machine) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, m.bin, args...)
	cmd.Env = m.env
	return cmd
}

// run executes one agent-mesh command and returns its combined output.
func (m *machine) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := m.command(ctx, args...).CombinedOutput()
	return string(out), err
}

func (m *machine) mustRun(args ...string) string {
	m.t.Helper()
	out, err := m.run(args...)
	if err != nil {
		m.t.Fatalf("agent-mesh %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (m *machine) token(name string) string {
	m.t.Helper()
	tok, err := config.ReadToken(filepath.Join(m.cfg, name))
	if err != nil {
		m.t.Fatal(err)
	}
	return tok
}

// sessions returns a client with this machine's session token.
func (m *machine) sessions(url string) *client.Client {
	return client.New(url, m.token("session.token"))
}

// lockedBuffer collects process output from another goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// startHub runs `agent-mesh hub` as m and waits for /healthz. It is stopped
// with SIGTERM at cleanup; its log is printed if the test failed.
func (m *machine) startHub(url string) {
	t := m.t
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := m.command(ctx, "hub")
	logs := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = logs, logs
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("hub log:\n%s", logs.String())
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(url + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("hub not healthy: %v\n%s", err, logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// freeAddr returns a loopback address nobody listens on right now.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// within polls cond every 50 ms until it holds or d passes.
func within(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", d, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
