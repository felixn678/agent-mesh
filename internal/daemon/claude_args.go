package daemon

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/felixn678/agent-mesh/internal/api"
)

// denyFloor is appended to every worker's --disallowedTools; not
// configurable. Absolute paths use the "//" prefix (verified against claude
// 2.1.301: an absolute-path Read deny blocks the read). The worktrees dir is
// deliberately not denied: it is where the worker works.
func denyFloor(stateDir string) []string {
	floor := []string{
		"Bash(git push*)", "Bash(gh *)", "Bash(curl *)", "Bash(wget *)", "WebFetch", "WebSearch",
	}
	private := []string{"~/.ssh/**", "~/.config/**", "~/.claude/**", "~/.claude.json"}
	for _, sub := range []string{"repos", "spool", "run", "logs"} {
		private = append(private, "//"+strings.TrimPrefix(filepath.Join(stateDir, sub), "/")+"/**")
	}
	for _, p := range private {
		floor = append(floor, "Read("+p+")", "Edit("+p+")")
	}
	return floor
}

// toolNames reduces allow rules to their tool names for --tools, so the
// worker does not even see tools outside its profile.
func toolNames(allow []string) []string {
	var out []string
	for _, a := range allow {
		name, _, _ := strings.Cut(a, "(")
		if !contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// appendPrompt is fixed text; task data never reaches the system prompt.
func appendPrompt(id int64) string {
	return fmt.Sprintf("Unattended agent-mesh task %d. Work only in this directory. Commit with clear messages. "+
		"Do not push or open PRs; do not create files outside the repo.", id)
}

// claudeArgs builds the worker argv (no shell). The prompt goes via stdin.
func claudeArgs(c Config, t api.ClaimedTask) []string {
	allow := c.ToolProfiles[t.ToolProfile].Allow
	args := []string{"-p", "--output-format", "stream-json", "--verbose",
		"--max-budget-usd", strconv.FormatFloat(t.MaxBudgetUSD, 'f', -1, 64),
		"--permission-mode", t.PermissionMode,
		"--permission-prompts", "none",
		"--strict-mcp-config",
		"--setting-sources", c.WorkerSettingSources,
		"--tools"}
	args = append(args, toolNames(allow)...)
	args = append(args, "--allowedTools")
	args = append(args, allow...)
	args = append(args, "--disallowedTools")
	args = append(args, denyFloor(c.StateDir)...)
	return append(args, "--append-system-prompt", appendPrompt(t.ID))
}
