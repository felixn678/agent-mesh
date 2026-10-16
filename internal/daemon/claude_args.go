package daemon

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/config"
)

// denyFloor is appended to every worker's --disallowedTools; not
// configurable. Absolute paths use the "//" prefix and may contain spaces
// (verified against claude 2.1.301 with "Application Support" in the path,
// both as dir/** and as an exact file). The worktrees dir is deliberately not
// denied: it is where the worker works.
func denyFloor(c Config) []string {
	floor := []string{
		"Bash(git push*)", "Bash(gh *)", "Bash(curl *)", "Bash(wget *)", "WebFetch", "WebSearch",
	}
	// Credentials, the agent's own config, and files that run code later as
	// this user (shell rc files, git config the daemon's own git reads, user
	// binaries, launch agents).
	private := []string{"~/.ssh/**", "~/.config/**", "~/.claude/**", "~/.claude.json",
		"~/.gitconfig", "~/.git-credentials", "~/.netrc", "~/.aws/**", "~/.gnupg/**",
		"~/.bashrc", "~/.bash_profile", "~/.profile", "~/.zshrc", "~/.zprofile", "~/.zshenv",
		"~/.local/bin/**", "~/Library/LaunchAgents/**"}
	for _, sub := range []string{"repos", "spool", "run", "logs"} {
		private = append(private, absRule(filepath.Join(c.StateDir, sub))+"/**")
	}
	// On the hub machine the default hub database (every task prompt and
	// session message) sits in the same state dir.
	private = append(private, absRule(c.StateDir)+"/hub.db*")
	// The agent-mesh config dirs (tokens, env files) wherever the OS puts
	// them, e.g. ~/Library/Application Support/agent-mesh on macOS, plus the
	// exact secret files in case they live elsewhere.
	worktrees := filepath.Join(c.StateDir, "worktrees")
	for _, dir := range []string{defaultConfigDir(), c.ConfigDir} {
		if dir != "" && !within(worktrees, dir) {
			private = append(private, absRule(dir)+"/**")
		}
	}
	for _, f := range []string{c.HostTokenFile, c.GHEnvFile, c.ClaudeEnvFile} {
		if f != "" && filepath.IsAbs(f) {
			private = append(private, absRule(f))
		}
	}
	seen := map[string]bool{}
	for _, p := range private {
		if !seen[p] {
			seen[p] = true
			floor = append(floor, "Read("+p+")", "Edit("+p+")")
		}
	}
	return floor
}

// absRule turns an absolute path into a permission-rule path ("//abs/path").
func absRule(p string) string { return "//" + strings.TrimPrefix(filepath.Clean(p), "/") }

// within reports whether path is dir or below it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// defaultConfigDir is the per-user agent-mesh config dir ("" if unknown).
func defaultConfigDir() string {
	dir, err := config.DefaultDir()
	if err != nil {
		return ""
	}
	return dir
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
	args = append(args, denyFloor(c)...)
	return append(args, "--append-system-prompt", appendPrompt(t.ID))
}
