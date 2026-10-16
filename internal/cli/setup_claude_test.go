package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/hubtest"
)

// fakeClaude writes a `claude` script that appends its argv (one arg per
// line, then "--") to a log and, for add-json/remove, edits a fake
// ~/.claude.json via python-free shell (it just records; state is seeded by
// the test). It returns the script and log paths.
func fakeClaude(t *testing.T, dir string) (bin, log string) {
	t.Helper()
	bin, log = filepath.Join(dir, "claude"), filepath.Join(dir, "claude-argv.log")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + log + "'; done\necho -- >> '" + log + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func setupFixture(t *testing.T) (setupEnv, string, *hubtest.Hub) {
	t.Helper()
	h := startHub(t)
	home := testEnv(t, h)
	claude, log := fakeClaude(t, t.TempDir())
	e := setupEnv{bin: testBin, claude: claude, home: home, hubURL: h.URL,
		settingsPath: filepath.Join(home, ".claude", "settings.json"), claudeJSON: filepath.Join(home, ".claude.json")}
	if err := os.MkdirAll(filepath.Dir(e.settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	return e, log, h
}

func runSetup(t *testing.T, e setupEnv, f setupFlags) (string, string) {
	t.Helper()
	var out, errOut syncBuf
	if err := setupClaude(bg, cmdIO{in: strings.NewReader(""), out: &out, err: &errOut}, e, f); err != nil {
		t.Fatalf("setup-claude %+v: %v\n%s", f, err, errOut.String())
	}
	return out.String(), errOut.String()
}

func readLog(t *testing.T, log string) string {
	b, _ := os.ReadFile(log)
	return string(b)
}

func TestSetupClaudeInstallIdempotentUninstall(t *testing.T) {
	e, log, _ := setupFixture(t)
	orig := fixture(t, "settings_claudekit.json")
	if err := os.WriteFile(e.settingsPath, []byte(orig), 0o640); err != nil {
		t.Fatal(err)
	}
	runSetup(t, e, setupFlags{})
	argv := readLog(t, log)
	if !strings.Contains(argv, "mcp\nadd-json\n-s\nuser\nagentmesh\n") ||
		!strings.Contains(argv, `"headersHelper":"'/opt/agent mesh/bin/agent-mesh' hook mcp-headers"`) ||
		strings.Contains(argv, "test-session-token") || strings.Contains(argv, "Bearer") {
		t.Fatalf("claude argv:\n%s", argv)
	}
	if b, _ := os.ReadFile(e.settingsPath + ".agentmesh-bak"); string(b) != orig {
		t.Fatal("backup must equal the pre-change bytes")
	}
	if st, _ := os.Stat(e.settingsPath); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	installed, _ := os.ReadFile(e.settingsPath)

	// Simulate claude having stored the server, then rerun: no change, no new backup.
	writeClaudeJSON(t, e, mcpConfig(e))
	_ = os.Remove(e.settingsPath + ".agentmesh-bak")
	_ = os.Remove(log)
	out, _ := runSetup(t, e, setupFlags{})
	if now, _ := os.ReadFile(e.settingsPath); string(now) != string(installed) || readLog(t, log) != "" ||
		!strings.Contains(out, "already up to date") || !strings.Contains(out, "already configured") {
		t.Fatalf("rerun changed something:\n%s\nargv: %s", out, readLog(t, log))
	}
	if _, err := os.Stat(e.settingsPath + ".agentmesh-bak"); !os.IsNotExist(err) {
		t.Fatal("idempotent run wrote a backup")
	}

	runSetup(t, e, setupFlags{uninstall: true})
	if now, _ := os.ReadFile(e.settingsPath); string(now) != orig {
		t.Fatalf("uninstall not byte-identical:\n%s", now)
	}
	if !strings.Contains(readLog(t, log), "mcp\nremove\n-s\nuser\nagentmesh\n") {
		t.Fatalf("uninstall argv: %s", readLog(t, log))
	}
}

func writeClaudeJSON(t *testing.T, e setupEnv, server map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"numStartups": 3, "mcpServers": map[string]any{"agentmesh": server, "other": map[string]any{"type": "stdio"}}})
	if err := os.WriteFile(e.claudeJSON, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSetupClaudeDryRunWritesNothing(t *testing.T) {
	e, log, _ := setupFixture(t)
	orig := fixture(t, "settings_claudekit.json")
	_ = os.WriteFile(e.settingsPath, []byte(orig), 0o600)
	out, _ := runSetup(t, e, setupFlags{dryRun: true})
	if !strings.Contains(out, "+++ ") || !strings.Contains(out, "+") || !strings.Contains(out, "would run: claude mcp add-json -s user agentmesh") {
		t.Fatalf("dry run output:\n%s", out)
	}
	if now, _ := os.ReadFile(e.settingsPath); string(now) != orig || readLog(t, log) != "" {
		t.Fatal("dry run changed files or ran claude")
	}
	if _, err := os.Stat(e.settingsPath + ".agentmesh-bak"); !os.IsNotExist(err) {
		t.Fatal("dry run wrote a backup")
	}
}

func TestSetupClaudeSymlinkMissingInvalidAndForce(t *testing.T) {
	e, log, _ := setupFixture(t)
	// Symlinked settings: target updated, link kept.
	target := filepath.Join(t.TempDir(), "real-settings.json")
	_ = os.WriteFile(target, []byte(`{"model":"opus"}`), 0o600)
	if err := os.Symlink(target, e.settingsPath); err != nil {
		t.Fatal(err)
	}
	writeClaudeJSON(t, e, map[string]any{"type": "http", "url": "http://elsewhere/mcp"})
	_, errOut := runSetup(t, e, setupFlags{})
	if fi, _ := os.Lstat(e.settingsPath); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	if b, _ := os.ReadFile(target); !strings.Contains(string(b), "hook session-start") {
		t.Fatalf("target not updated: %s", b)
	}
	if !strings.Contains(errOut, "--force") || strings.Contains(readLog(t, log), "add-json") {
		t.Fatalf("different server must need --force: %s / %s", errOut, readLog(t, log))
	}
	runSetup(t, e, setupFlags{force: true})
	if argv := readLog(t, log); !strings.Contains(argv, "remove\n-s\nuser\nagentmesh\n--\nmcp\nadd-json") {
		t.Fatalf("force argv: %s", argv)
	}

	// Invalid JSON: error, file untouched.
	_ = os.Remove(e.settingsPath)
	_ = os.WriteFile(e.settingsPath, []byte(`{"hooks": {`), 0o600)
	var out syncBuf
	if err := setupClaude(bg, cmdIO{in: strings.NewReader(""), out: &out, err: &out}, e, setupFlags{}); err == nil {
		t.Fatal("invalid JSON must fail")
	}
	if b, _ := os.ReadFile(e.settingsPath); string(b) != `{"hooks": {` {
		t.Fatal("invalid file touched")
	}

	// Missing file: created with only our hooks.
	_ = os.Remove(e.settingsPath)
	runSetup(t, e, setupFlags{})
	if b, _ := os.ReadFile(e.settingsPath); !json.Valid(b) || strings.Count(string(b), " hook ") != 4 {
		t.Fatalf("created settings: %s", b)
	}
}

func TestSetupClaudeRequiresSessionToken(t *testing.T) {
	e, _, _ := setupFixture(t)
	p, _ := configFile(sessionTokenFile)
	_ = os.Remove(p)
	var out syncBuf
	if err := setupClaude(bg, cmdIO{in: strings.NewReader(""), out: &out, err: &out}, e, setupFlags{}); err == nil ||
		!strings.Contains(err.Error(), "session token") {
		t.Fatalf("want session token error, got %v", err)
	}
	if _, err := os.Stat(e.settingsPath); !os.IsNotExist(err) {
		t.Fatal("settings written without a token")
	}
}
