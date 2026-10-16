package hub

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/config"
)

func writeHubTOML(t *testing.T, dir, listen string) string {
	t.Helper()
	path := filepath.Join(dir, "hub.toml")
	body := fmt.Sprintf("listen = %q\ndb_path = %q\nadmin_token_hash = %q\n",
		listen, filepath.Join(dir, "hub.db"), config.HashToken("admin-token"))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A SIGHUP that arrives while the hub is still waiting for its address must
// not terminate it; once the address frees up the hub serves and, on clean
// stop, removes its pidfile.
func TestSIGHUPDuringBindRetryKeepsHubUp(t *testing.T) {
	old := bindRetryEvery
	bindRetryEvery = 100 * time.Millisecond
	t.Cleanup(func() { bindRetryEvery = old })

	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := blocker.Addr().String()
	dir := t.TempDir()
	path := writeHubTOML(t, dir, addr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, []string{"--config", path}) }()

	time.Sleep(300 * time.Millisecond) // inside the bind-retry loop
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("hub exited after SIGHUP: %v", err)
	default:
	}
	_ = blocker.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hub never came up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, PidFileName)); err != nil {
		t.Errorf("pidfile missing while running: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("hub stop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, PidFileName)); !os.IsNotExist(err) {
		t.Errorf("pidfile left behind after clean exit: %v", err)
	}
}

func TestIsHubArgs(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"/home/u/bin/agent-mesh", "hub"}, true},
		{[]string{"agent-mesh", "hub", "--config", "x"}, true},
		{[]string{"agent-mesh", "daemon"}, false},
		{[]string{"-zsh"}, false},
		{[]string{"tmux", "hub"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isHubArgs(c.args); got != c.want {
			t.Errorf("isHubArgs(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

// SignalReload must refuse a pid that is alive but is not a hub, and must
// leave that process untouched.
func TestSignalReloadRefusesUnrelatedProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip("sleep unavailable:", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	pidPath := filepath.Join(t.TempDir(), PidFileName)
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	err := SignalReload(pidPath)
	if err == nil || !strings.Contains(err.Error(), "not an agent-mesh hub process") {
		t.Fatalf("err = %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if cmd.ProcessState != nil {
		t.Fatal("unrelated process was signalled")
	}
}

func TestSignalReloadReportsDeadPid(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), PidFileName)
	if err := os.WriteFile(pidPath, []byte("2147483646\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SignalReload(pidPath); err == nil {
		t.Fatal("dead pid accepted")
	}
}
