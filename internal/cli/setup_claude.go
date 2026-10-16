package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// mcpName is the user-scope MCP server name; this repo's .mcp.json uses the
// same name so the project entry shadows it here.
const mcpName = "agentmesh"

// setupEnv holds every path setup-claude touches (tests point them at a temp HOME).
type setupEnv struct {
	bin          string // absolute agent-mesh path written into hooks and the MCP helper
	claude       string // claude executable ("" = not on PATH)
	home         string // working dir for `claude mcp` (avoids project-scope servers)
	settingsPath string // ~/.claude/settings.json
	claudeJSON   string // ~/.claude.json (read-only here; changed only via `claude mcp`)
	hubURL       string
}

type setupFlags struct{ uninstall, dryRun, force bool }

func defaultSetupEnv() (setupEnv, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return setupEnv{}, err
	}
	e := setupEnv{bin: selfBin(), home: home, hubURL: hubURL(),
		settingsPath: filepath.Join(home, ".claude", "settings.json"), claudeJSON: filepath.Join(home, ".claude.json")}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		e.settingsPath, e.claudeJSON = filepath.Join(dir, "settings.json"), filepath.Join(dir, ".claude.json")
	}
	e.claude, _ = exec.LookPath("claude")
	return e, nil
}

func runSetupClaude(ctx context.Context, std cmdIO, args []string) error {
	fs := newFlags("setup-claude", std)
	var f setupFlags
	fs.BoolVar(&f.uninstall, "uninstall", false, "remove only the agentmesh hooks and MCP server")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print the settings diff and claude commands; change nothing")
	fs.BoolVar(&f.force, "force", false, "replace an existing, different agentmesh MCP server")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := defaultSetupEnv()
	if err != nil {
		return err
	}
	return setupClaude(ctx, std, e, f)
}

// setupClaude installs (or removes) the agentmesh hooks in settings.json and
// the user-scope MCP server. It never touches anything else in ~/.claude.
func setupClaude(ctx context.Context, std cmdIO, e setupEnv, f setupFlags) error {
	if e.claude == "" && !f.dryRun {
		return errors.New("setup-claude: `claude` not found on PATH")
	}
	if !f.uninstall {
		if _, err := readSessionToken(); err != nil && !f.dryRun {
			return fmt.Errorf("setup-claude: session token required (agent-mesh token new --session <host> --out <config-dir>/session.token): %w", err)
		}
		checkHub(ctx, std, e.hubURL)
	}
	if err := editSettings(std, e, f); err != nil {
		return err
	}
	if err := editMCP(ctx, std, e, f); err != nil {
		return err
	}
	if !f.uninstall && !f.dryRun {
		fmt.Fprintln(std.out, "done. Restart running Claude sessions (hooks load at session start; review with /hooks if asked).")
		fmt.Fprintln(std.out, "Optional: export AGENTMESH_SESSION_NAME=<name> before starting claude to name a session.")
	}
	return nil
}

func checkHub(ctx context.Context, std cmdIO, url string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
	if err != nil || resp.StatusCode != http.StatusOK {
		fmt.Fprintf(std.err, "warning: hub %s not reachable; hooks stay silent until it is\n", url)
	}
}

func editSettings(std cmdIO, e setupEnv, f setupFlags) error {
	target := e.settingsPath
	if r, err := filepath.EvalSymlinks(target); err == nil {
		target = r // write through a symlinked settings.json, keep the link
	}
	orig, err := os.ReadFile(target)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	edit := Merge
	if f.uninstall {
		edit = Unmerge
	}
	next, changed, err := edit(orig, e.bin)
	if err != nil {
		return fmt.Errorf("setup-claude: %s: %w (file left untouched)", target, err)
	}
	switch {
	case !changed:
		fmt.Fprintf(std.out, "hooks: %s already up to date\n", target)
		return nil
	case f.dryRun:
		fmt.Fprintf(std.out, "--- %s\n+++ %s (proposed)\n%s", target, target, lineDiff(string(orig), string(next)))
		return nil
	}
	mode := os.FileMode(0o600)
	if exists {
		st, err := os.Stat(target)
		if err != nil {
			return err
		}
		mode = st.Mode().Perm()
		if err := os.WriteFile(e.settingsPath+".agentmesh-bak", orig, 0o600); err != nil {
			return fmt.Errorf("backup settings: %w", err)
		}
	} else if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if err := writeAtomic(target, next, mode); err != nil {
		return err
	}
	fmt.Fprintf(std.out, "hooks: updated %s (backup %s.agentmesh-bak)\n", target, e.settingsPath)
	return nil
}

// writeAtomic replaces path via a temp file in the same directory.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agentmesh-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// mcpConfig is the token-free user-scope server: Claude runs the
// headersHelper to obtain the bearer from session.token.
func mcpConfig(e setupEnv) map[string]any {
	return map[string]any{"type": "http", "url": e.hubURL + "/mcp", "headersHelper": hookCommand(e.bin, "mcp-headers")}
}

// currentMCP reads the user-scope agentmesh entry (nil if absent).
func currentMCP(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg.MCPServers[mcpName], nil
}

func editMCP(ctx context.Context, std cmdIO, e setupEnv, f setupFlags) error {
	cur, err := currentMCP(e.claudeJSON)
	if err != nil {
		return err
	}
	want := mcpConfig(e)
	wantJSON, _ := json.Marshal(want)
	remove := []string{"mcp", "remove", "-s", "user", mcpName}
	add := []string{"mcp", "add-json", "-s", "user", mcpName, string(wantJSON)}
	var cmds [][]string
	switch {
	case f.uninstall && cur == nil:
		fmt.Fprintln(std.out, "mcp: no user-scope agentmesh server")
	case f.uninstall:
		cmds = [][]string{remove}
	case reflect.DeepEqual(cur, want):
		fmt.Fprintln(std.out, "mcp: agentmesh already configured")
	case cur != nil && !f.force:
		fmt.Fprintln(std.err, "warning: a different user-scope agentmesh MCP server exists; rerun with --force to replace it")
	case cur != nil:
		cmds = [][]string{remove, add}
	default:
		cmds = [][]string{add}
	}
	for _, argv := range cmds {
		if f.dryRun {
			fmt.Fprintf(std.out, "would run: claude %s\n", shellJoin(argv))
			continue
		}
		cmd := exec.CommandContext(ctx, e.claude, argv...) // argv, no shell
		cmd.Dir = e.home
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("claude %s: %w: %s", argv[1], err, bytes.TrimSpace(out.Bytes()))
		}
		fmt.Fprintf(std.out, "mcp: ran claude %s\n", shellJoin(argv[:min(len(argv), 5)]))
	}
	return nil
}

func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}
