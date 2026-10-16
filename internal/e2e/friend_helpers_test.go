package e2e_test

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// claudeKitSettings is a realistic ~/.claude/settings.json of a ClaudeKit
// user: hooks on several events, a status line, permissions and plugins.
// setup-claude must only add to it and --uninstall must restore it exactly.
const claudeKitSettings = `{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "includeCoAuthoredBy": false,
  "statusLine": {
    "type": "command",
    "command": "node \"$HOME/.claude/statusline.cjs\"",
    "padding": 0
  },
  "hooks": {
    "SessionStart": [
      {
        "matcher": "startup|resume|clear|compact",
        "hooks": [
          { "type": "command", "command": "node \"$HOME/.claude/hooks/session-init.cjs\"" }
        ]
      }
    ],
    "UserPromptSubmit": [
      { "hooks": [ { "type": "command", "command": "node \"$HOME/.claude/hooks/dev-rules-reminder.cjs\"" } ] }
    ],
    "PreToolUse": [
      {
        "matcher": "Bash|Glob|Grep|Read|Edit|Write",
        "hooks": [
          { "type": "command", "command": "node \"$HOME/.claude/hooks/scout-block.cjs\"" },
          { "type": "command", "command": "node \"$HOME/.claude/hooks/privacy-block.cjs\"" }
        ]
      }
    ],
    "SubagentStart": [
      { "hooks": [ { "type": "command", "command": "node \"$HOME/.claude/hooks/subagent-init.cjs\"" } ] }
    ]
  },
  "permissions": { "allow": ["Bash(git status)"], "deny": [] },
  "enabledPlugins": { "example@marketplace": true }
}
`

// realRelease packs the freshly built binary the way scripts/build-release.sh
// does (agent-mesh-<os>-<arch>.tar.gz + SHA256SUMS) so install.sh can install
// it from a file:// base.
func realRelease(t *testing.T, bin string) string {
	t.Helper()
	dir := t.TempDir()
	asset := filepath.Join(dir, "agent-mesh-"+runtime.GOOS+"-"+runtime.GOARCH+".tar.gz")
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(asset)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "agent-mesh", Mode: 0o755, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	for _, c := range []interface{ Close() error }{tw, gz, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	packed, _ := os.ReadFile(asset)
	sum := sha256.Sum256(packed)
	line := hex.EncodeToString(sum[:]) + "  " + filepath.Base(asset) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// friendTools is a PATH for a "friend's machine": stub claude (records its
// argv to $HOME/claude-argv.log), gh and gitleaks first, then the system dirs
// so the hooks can use the real git to read a repo's remote.
func friendTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	stubs := map[string]string{
		"claude":   "#!/bin/sh\necho \"$@\" >> \"$HOME/claude-argv.log\"\nexit 0\n",
		"gh":       "#!/bin/sh\nexit 0\n",
		"gitleaks": "#!/bin/sh\nexit 0\n",
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	return dir + ":" + filepath.Dir(git) + ":/usr/bin:/bin"
}

// gitRepo creates an empty repo in dir whose origin is remote.
func gitRepo(t *testing.T, dir, remote string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q", dir}, {"-C", dir, "remote", "add", "origin", remote}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}
