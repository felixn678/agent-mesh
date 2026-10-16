package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
)

func TestTaskAddNamesTheBudgetFlag(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	for args, want := range map[string]string{
		"":              "--budget <usd> is required",
		"--budget=-1":   "--budget must be > 0 and <= 100",
		"--budget=1000": "--budget must be > 0 and <= 100",
	} {
		argv := []string{"task", "add", "--repo", "r", "--prompt", "p"}
		if args != "" {
			argv = append(argv, args)
		}
		if _, _, err := runCLI(t, "", argv...); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: got %v, want %q", args, err, want)
		}
	}
}

func TestCancelWithoutAdminTokenExplainsWhereToRunIt(t *testing.T) {
	h := startHub(t)
	testEnv(t, h) // session token only, like a joined machine
	runCLI(t, "", "task", "add", "--repo", "r", "--prompt", "p", "--budget", "1")
	for _, op := range []string{"cancel", "requeue"} {
		_, _, err := runCLI(t, "", "task", op, "1")
		if err == nil || !strings.Contains(err.Error(), "needs the admin token, which exists only on the hub machine") ||
			!strings.Contains(err.Error(), "403") {
			t.Fatalf("%s: %v", op, err)
		}
	}
}

func TestHostsWithoutDaemonsNamesMachinesWithSessions(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	for host, ids := range map[string][]string{"host-a": {"aa-000001", "aa-000002"}, "host-b": {"bb-000001"}} {
		for _, id := range ids {
			if _, err := h.Session(host).RegisterSession(bg, api.RegisterSessionRequest{ID: id, Cwd: "/w/" + id}); err != nil {
				t.Fatal(err)
			}
		}
	}
	out, _, err := runCLI(t, "", "hosts")
	if err != nil || !strings.Contains(out, "no daemon has reported load yet") ||
		!strings.Contains(out, "machines with live Claude sessions: host-a (2), host-b (1)") {
		t.Fatalf("%q %v", out, err)
	}
	out, _, err = runCLI(t, "", "sessions")
	if err != nil || !strings.HasPrefix(out, "sessions (3 live):\n") || !strings.Contains(out, "bb-00000 host-b") {
		t.Fatalf("sessions: %q %v", out, err)
	}
	if _, _, err := runCLI(t, "", "sessions", "extra"); err == nil {
		t.Fatal("sessions takes no arguments")
	}
}

func TestTaskListAndGetShowTarget(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	runCLI(t, "", "task", "add", "--repo", "r", "--prompt", "p", "--budget", "1", "--host", "laptop")
	out, _, err := runCLI(t, "", "task", "list")
	if err != nil || !strings.Contains(out, " target=laptop ") {
		t.Fatalf("list: %q %v", out, err)
	}
	out, _, err = runCLI(t, "", "task", "get", "1")
	if err != nil || !strings.Contains(out, "\ntarget_host: laptop\n") || strings.Contains(out, "\nhost: ") {
		t.Fatalf("get: %q %v", out, err)
	}
}

// TestHookCommandRunsFromPathWithSpaces executes the generated hook command
// through a real shell, as Claude Code does, with the binary in a directory
// whose name contains a space, and checks ownership still round-trips.
func TestHookCommandRunsFromPathWithSpaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "John Smith", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "agent-mesh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"ran $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := hookCommand(bin, "prompt")
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ran hook prompt "+managedFlag {
		t.Fatalf("sh -c %q: %v %q", cmd, err, out)
	}
	if ev, ok := managedEvent(cmd); !ok || ev != "prompt" {
		t.Fatalf("managedEvent(%q) = %q %v", cmd, ev, ok)
	}
	if !managedMCP(map[string]any{"headersHelper": hookCommand(bin, "mcp-headers")}) {
		t.Fatal("MCP headersHelper with a spaced path not recognised as ours")
	}
	merged, _, created, err := Merge([]byte("{}\n"), bin)
	if err != nil {
		t.Fatal(err)
	}
	if again, changed, _, _ := Merge(merged, bin); changed || string(again) != string(merged) {
		t.Fatal("rerun with a spaced path is not idempotent")
	}
	if back, _, err := Unmerge(merged, created); err != nil || string(back) != "{}\n" {
		t.Fatalf("unmerge: %q %v", back, err)
	}
}
