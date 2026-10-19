package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// commandMarker marks the slash-command files setup-claude writes; files
// without it are the user's and are never changed or removed (without --force).
const commandMarker = "<!-- managed-by=agent-mesh -->"

// The /am:on and /am:off commands. The prompt hook sees the typed command
// before Claude expands it and does the switching; the body only tells
// Claude where its instructions are.
var meshCommands = map[string]string{
	"on.md": "---\ndescription: Put this session in the agent-mesh (talk to sessions on your other machines)\n---\n" + commandMarker + "\n" +
		"Follow the [agentmesh] instructions added to this prompt. If there are none, the agent-mesh hook did not run: " +
		"tell the user to run `agent-mesh setup-claude` in a terminal and restart this session.\n",
	"off.md": "---\ndescription: Take this session out of the agent-mesh\n---\n" + commandMarker + "\n" +
		"Follow the [agentmesh] instructions added to this prompt. If there are none, the agent-mesh hook did not run: " +
		"tell the user to run `agent-mesh setup-claude` in a terminal and restart this session.\n",
}

// commandsDir holds /am:on and /am:off: commands/am next to settings.json.
func commandsDir(e setupEnv) string {
	return filepath.Join(filepath.Dir(e.settingsPath), "commands", "am")
}

// editCommands writes (or removes) the /am:on and /am:off command files.
func editCommands(std cmdIO, e setupEnv, f setupFlags) error {
	dir := commandsDir(e)
	for _, name := range []string{"on.md", "off.md"} {
		path, want := filepath.Join(dir, name), meshCommands[name]
		cur, err := os.ReadFile(path)
		exists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		ours := exists && strings.Contains(string(cur), commandMarker)
		var action string
		switch {
		case f.uninstall && !exists, !f.uninstall && string(cur) == want:
			continue
		case exists && !ours && !f.force:
			fmt.Fprintf(std.err, "warning: %s was not written by setup-claude; left in place (--force replaces it)\n", path)
			continue
		case f.uninstall:
			action = "removed"
		default:
			action = "wrote"
		}
		if f.dryRun {
			fmt.Fprintf(std.out, "would have %s %s\n", action, path)
			continue
		}
		if f.uninstall {
			err = os.Remove(path)
		} else if err = os.MkdirAll(dir, 0o700); err == nil {
			err = writeAtomic(path, []byte(want), 0o644)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(std.out, "commands: %s %s\n", action, path)
	}
	if f.uninstall && !f.dryRun {
		_ = os.Remove(dir) // only when empty
	}
	return nil
}
