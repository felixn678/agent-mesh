package format

import (
	"fmt"
	"strings"

	"github.com/felixn678/agent-mesh/internal/api"
)

// HostProfileLine is one machine for humans and sessions, e.g.
// `mac (macos/arm64, 3 live sessions) note: "frontend, Expo" tools: xcode, adb`.
// The note is the user's own text, quoted and stripped of control characters.
func HostProfileLine(h api.HostProfile) string {
	var b strings.Builder
	b.WriteString(Clean(h.Name))
	var meta []string
	if h.OS != "" {
		meta = append(meta, Clean(h.OS))
	}
	sessions := "sessions"
	if h.LiveSessions == 1 {
		sessions = "session"
	}
	meta = append(meta, fmt.Sprintf("%d live %s", h.LiveSessions, sessions))
	b.WriteString(" (" + strings.Join(meta, ", ") + ")")
	if h.Note != "" {
		fmt.Fprintf(&b, " note: %q", Clean(h.Note))
	} else {
		b.WriteString(" no note")
	}
	if len(h.Tools) > 0 {
		b.WriteString(" tools: " + Clean(strings.Join(h.Tools, ", ")))
	}
	return b.String()
}

// MeshMachines is the session-start briefing on the other machines and how to
// route work between them; "" when the mesh has a single machine.
func MeshMachines(list []api.HostProfile, self string) string {
	if len(list) < 2 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[agentmesh] machines in the mesh (notes are the user's description of each machine's role; tools are detected):\n")
	for _, h := range list {
		line := HostProfileLine(h)
		if h.Name == self {
			line = strings.Replace(line, Clean(h.Name), Clean(h.Name)+" [this machine]", 1)
		}
		b.WriteString("  - " + line + "\n")
	}
	b.WriteString("Routing: a machine the user names always wins. Otherwise, when a request fits another machine clearly better " +
		"than this one (by its note and tools, e.g. iOS builds and simulators need xcode, Android emulators need adb), " +
		"queue it there with task_add(target_host=<machine>, from=<your session id>) and tell the user which machine and why; " +
		"do the work yourself when this machine fits. A queued task runs only once a live session (or daemon) on that machine " +
		"has the same repo open: check `sessions`, and if there is none, say the task waits until one is opened there.")
	return b.String()
}
