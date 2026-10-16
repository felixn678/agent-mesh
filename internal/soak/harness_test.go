//go:build soak

package soak

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/config"
)

// buildBinary compiles the real agent-mesh binary.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agent-mesh")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/felixn678/agent-mesh/cmd/agent-mesh")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// machine is one fake user account (own HOME and config dir); commands never
// inherit the caller's HOME or AGENTMESH_* variables.
type machine struct {
	t    *testing.T
	bin  string
	home string
	cfg  string
	env  []string
}

func newMachine(t *testing.T, bin string) *machine {
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	return &machine{t: t, bin: bin, home: home, cfg: filepath.Join(xdg, "agent-mesh"),
		env: []string{"HOME=" + home, "XDG_CONFIG_HOME=" + xdg, "PATH=/usr/bin:/bin", "TMPDIR=" + os.TempDir()}}
}

func (m *machine) run(args ...string) string {
	m.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.bin, args...)
	cmd.Env = m.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		m.t.Fatalf("agent-mesh %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (m *machine) token(name string) string {
	m.t.Helper()
	tok, err := config.ReadToken(filepath.Join(m.cfg, name))
	if err != nil {
		m.t.Fatal(err)
	}
	return tok
}

// fastTimings shortens leases and reaping so dead sessions' tasks come back
// within the run (the real binary has no fake clock).
func (m *machine) fastTimings() {
	m.t.Helper()
	p := filepath.Join(m.cfg, "hub.toml")
	b, err := os.ReadFile(p)
	if err != nil {
		m.t.Fatal(err)
	}
	s := string(b)
	for key, val := range map[string]string{"lease_ttl": "8s", "reap_interval": "1s", "revive_window": "4s",
		"stale_requeue": "30s"} {
		re := regexp.MustCompile(`(?m)^` + key + ` = .*$`)
		if !re.MatchString(s) {
			m.t.Fatalf("hub.toml has no %s", key)
		}
		s = re.ReplaceAllString(s, fmt.Sprintf("%s = %q", key, val))
	}
	s = strings.Replace(s, "\n[host_tokens]", "\nsession_lease_ttl = \"8s\"\n\n[host_tokens]", 1)
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) dbPath() string {
	return filepath.Join(m.home, ".local", "share", "agent-mesh", "hub.db")
}

// hubProc is the running `agent-mesh hub`; its output is appended to one
// log file across restarts.
type hubProc struct {
	m    *machine
	url  string
	log  *os.File
	cmd  *exec.Cmd
	done chan struct{}
}

func freePort(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func (h *hubProc) start() {
	t := h.m.t
	t.Helper()
	cmd := exec.Command(h.m.bin, "hub")
	cmd.Env, cmd.Stdout, cmd.Stderr = h.m.env, h.log, h.log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h.cmd, h.done = cmd, make(chan struct{})
	go func() { _ = cmd.Wait(); close(h.done) }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get(h.url + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("hub did not become healthy: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stop sends SIGTERM and waits for a clean exit.
func (h *hubProc) stop() {
	h.m.t.Helper()
	_ = h.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-h.done:
	case <-time.After(20 * time.Second):
		_ = h.cmd.Process.Kill()
		h.m.t.Errorf("hub did not exit within 20s of SIGTERM")
		<-h.done
	}
	if code := h.cmd.ProcessState.ExitCode(); code != 0 {
		h.m.t.Errorf("hub exit code %d after SIGTERM", code)
	}
}

func (h *hubProc) pid() int { return h.cmd.Process.Pid }

// procStats reads the open FD and thread counts of pid.
func procStats(pid int) (fds, threads int) {
	ents, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	fds = len(ents)
	b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "Threads:"); ok {
			fmt.Sscan(strings.TrimSpace(v), &threads)
		}
	}
	return fds, threads
}

// bundle is the decoded invite line.
type bundle struct {
	URL          string `json:"url"`
	Host         string `json:"host"`
	SessionToken string `json:"session_token"`
}

var joinRE = regexp.MustCompile(`agent-mesh join (\S+) (\S+)`)

// invite runs `invite <host>` on the hub machine and returns url + bundle.
func invite(m *machine, host, url string) (joinURL, raw string, b bundle) {
	m.t.Helper()
	out := m.run("invite", host, "--url", url)
	sub := joinRE.FindStringSubmatch(out)
	if sub == nil {
		m.t.Fatalf("no join line in:\n%s", out)
	}
	dec, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(sub[2], "="))
	if err != nil {
		m.t.Fatalf("bundle: %v", err)
	}
	if err := json.Unmarshal(dec, &b); err != nil {
		m.t.Fatalf("bundle json: %v", err)
	}
	return sub[1], sub[2], b
}
