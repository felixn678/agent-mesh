package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFriendInstallsAndUsesMesh follows the README as a newcomer would, on two
// fake machines: install from a release, `up` on the hub, `invite` + `join`
// (code pasted on stdin), opt-in `setup-claude` over existing ClaudeKit
// settings and its exact uninstall, `doctor`, then a task targeted at the
// second machine waking an idle session there.
func TestFriendInstallsAndUsesMesh(t *testing.T) {
	built, path := buildBinary(t), friendTools(t)
	release := realRelease(t, built)
	hub, laptop := newMachine(t, built, path), newMachine(t, built, path)
	for _, m := range []*machine{hub, laptop} {
		home := strings.TrimPrefix(m.env[0], "HOME=")
		out, err := runInstaller(t, home, "AGENTMESH_DOWNLOAD_BASE=file://"+release)
		if err != nil {
			t.Fatalf("install.sh: %v\n%s", err, out)
		}
		m.bin = filepath.Join(home, ".local", "bin", "agent-mesh")
		if fi, err := os.Stat(m.bin); err != nil || fi.Mode().Perm() != 0o755 {
			t.Fatalf("installed binary: %v %v", fi, err)
		}
	}

	addr := freeAddr(t)
	url := "http://" + addr
	hub.mustRun("up", "--no-service", "--name", "ubuntu", "--listen", addr)
	hub.startHub(url)

	m := joinLineRE.FindStringSubmatch(hub.mustRun("invite", "laptop"))
	if m == nil || m[1] != url {
		t.Fatal("invite printed no join line for the hub URL")
	}
	join := laptop.command(context.Background(), "join", url, "--no-service")
	join.Stdin = strings.NewReader(m[2] + "\n")
	if out, err := join.CombinedOutput(); err != nil {
		t.Fatalf("join with code on stdin: %v\n%s", err, out)
	}
	if strings.Contains(strings.Join(join.Args, " "), m[2]) {
		t.Fatal("invite code leaked into argv")
	}

	t.Run("setup-claude only adds and uninstalls exactly", func(t *testing.T) { setupClaudeRoundTrip(t, laptop) })

	t.Run("doctor passes on both machines", func(t *testing.T) {
		for _, mc := range []*machine{hub, laptop} {
			out := mc.mustRun("doctor")
			for _, want := range []string{"✓ hub reachable", "✓ auth session", "✓ auth host"} {
				if !strings.Contains(out, want) {
					t.Fatalf("doctor missing %q:\n%s", want, out)
				}
			}
		}
	})

	t.Run("targeted task wakes an idle session on the laptop", func(t *testing.T) {
		out := hub.mustRun("task", "add", "--repo", "git@github.com:acme/lumio.git",
			"--prompt", "fix the flaky test", "--budget", "2", "--host", "laptop")
		if !strings.Contains(out, "repo="+lumio) {
			t.Fatalf("task add did not canonicalize the repo:\n%s", out)
		}
		repo := filepath.Join(t.TempDir(), "lumio")
		gitRepo(t, repo, "https://github.com/acme/lumio.git") // another remote spelling
		const sid = "b1b1b1b1-0000-4000-8000-000000000001"
		start := laptop.command(context.Background(), "hook", "session-start")
		start.Dir = repo
		start.Stdin = strings.NewReader(`{"session_id":"` + sid + `","cwd":"` + repo + `","hook_event_name":"SessionStart","source":"startup"}`)
		ctxOut, err := start.Output()
		if err != nil || !strings.Contains(string(ctxOut), "inbox --wait --session "+sid) {
			t.Fatalf("session-start: %v\n%s", err, ctxOut)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		began := time.Now()
		wait, err := laptop.command(ctx, "inbox", "--wait", "--session", sid).CombinedOutput()
		if err != nil || !strings.Contains(string(wait), "can be claimed by this session") {
			t.Fatalf("waiter: %v\n%s", err, wait)
		}
		if d := time.Since(began); d > 5*time.Second {
			t.Fatalf("waiter took %v to report claimable work", d)
		}
	})
}

// setupClaudeRoundTrip checks the opt-in Claude integration on a ClaudeKit-like
// settings file: dry run changes nothing, setup only adds lines, a rerun is a
// no-op, and --uninstall restores the original bytes.
func setupClaudeRoundTrip(t *testing.T, m *machine) {
	home := strings.TrimPrefix(m.env[0], "HOME=")
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(claudeKitSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	read := func() string { b, _ := os.ReadFile(settings); return string(b) }

	m.mustRun("setup-claude", "--dry-run")
	if read() != claudeKitSettings {
		t.Fatal("--dry-run changed settings.json")
	}
	m.mustRun("setup-claude")
	after := read()
	for _, line := range strings.Split(claudeKitSettings, "\n") {
		if strings.TrimSpace(line) != "" && !strings.Contains(after, strings.TrimSpace(strings.TrimSuffix(line, ","))) {
			t.Fatalf("setup removed or rewrote an existing line: %q", line)
		}
	}
	for _, ev := range []string{"hook session-start", "hook prompt", "hook stop", "hook session-end"} {
		if !strings.Contains(after, ev) {
			t.Fatalf("setup did not add %q", ev)
		}
	}
	argv, _ := os.ReadFile(filepath.Join(home, "claude-argv.log"))
	if !strings.Contains(string(argv), "mcp add-json -s user agentmesh") || strings.Contains(string(argv), m.token("session.token")) {
		t.Fatalf("unexpected claude invocation (token must never be passed):\n%s", argv)
	}
	m.mustRun("setup-claude")
	if read() != after {
		t.Fatal("second setup-claude changed settings.json")
	}
	m.mustRun("setup-claude", "--uninstall")
	if read() != claudeKitSettings {
		t.Fatalf("--uninstall did not restore the original bytes:\n%s", read())
	}
}
