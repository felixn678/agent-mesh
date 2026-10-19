package cli

import (
	"os"
	"path/filepath"
	"strings"
)

// A session takes part in the mesh only when the user turns it on: running
// /am:on in it, or starting claude with AGENTMESH=on. Every other
// session's hooks return at once without reading a token or calling the hub,
// so ordinary sessions keep working (and messaging each other) as plain
// Claude Code sessions.

// meshToggle returns "on" or "off" when the prompt is the /am:on or /am:off
// command (the hook sees the command as typed, before Claude expands it),
// else "".
func meshToggle(prompt string) string {
	fields := strings.Fields(prompt)
	if len(fields) == 0 {
		return ""
	}
	switch strings.ToLower(fields[0]) {
	case "/am:on":
		return "on"
	case "/am:off":
		return "off"
	}
	return ""
}

func meshMarkPath(id string) string {
	dir, err := stateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "mesh", id)
}

// meshOn reports whether the session is in the mesh. A recorded /am:on or
// /am:off wins (it survives resume and compaction); without one,
// AGENTMESH=on in claude's environment turns it on.
func meshOn(id string) bool {
	if p := meshMarkPath(id); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			return strings.TrimSpace(string(b)) == "on"
		}
	}
	return os.Getenv("AGENTMESH") == "on"
}

// setMeshOn records the user's choice for the session.
func setMeshOn(id string, on bool) error {
	p := meshMarkPath(id)
	if p == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	state := "off"
	if on {
		state = "on"
	}
	return os.WriteFile(p, []byte(state+"\n"), 0o600)
}
