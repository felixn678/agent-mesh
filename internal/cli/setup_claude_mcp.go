package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
)

// mcpConfig is the token-free user-scope server: Claude runs the
// headersHelper to obtain the bearer from session.token. The URL comes from
// the config files only (never $AGENTMESH_URL), like every session-token client.
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
	ours := cur != nil && managedMCP(cur)
	var cmds [][]string
	switch {
	case f.uninstall && cur == nil:
		fmt.Fprintln(std.out, "mcp: no user-scope agentmesh server")
	case f.uninstall && !ours && !f.force:
		fmt.Fprintln(std.err, "warning: the user-scope agentmesh MCP server was not written by setup-claude; left in place (--force removes it)")
	case f.uninstall:
		cmds = [][]string{remove}
	case reflect.DeepEqual(cur, want):
		fmt.Fprintln(std.out, "mcp: agentmesh already configured")
	case cur != nil && !ours && !f.force:
		fmt.Fprintln(std.err, "warning: a user-scope agentmesh MCP server not written by setup-claude exists; rerun with --force to replace it")
	case cur != nil: // ours with a new url or binary path, or forced
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

// managedMCP reports whether an MCP entry is ours: its headersHelper is a
// managed `agent-mesh hook mcp-headers` command.
func managedMCP(cfg map[string]any) bool {
	h, _ := cfg["headersHelper"].(string)
	ev, ok := managedEvent(h)
	return ok && ev == "mcp-headers"
}
