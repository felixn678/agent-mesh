package onboard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// serviceTools are the programs workers run; the service PATH must reach them.
var serviceTools = []string{"claude", "git", "gh", "gitleaks"}

// servicePath builds the PATH written into the service units: the directories
// of the tools found on the current PATH (so an nvm or npm-global claude keeps
// working under systemd/launchd), then the usual locations.
func (e *Env) servicePath() string {
	var dirs []string
	for _, t := range serviceTools {
		if p, err := e.Runner.LookPath(t); err == nil && filepath.IsAbs(p) {
			dirs = append(dirs, filepath.Dir(p))
		}
	}
	dirs = append(dirs, filepath.Join(e.Home, ".local", "bin"), filepath.Join(e.Home, "go", "bin"))
	if e.GOOS == "darwin" {
		dirs = append(dirs, "/opt/homebrew/bin")
	}
	dirs = append(dirs, "/usr/local/bin", "/usr/bin", "/bin")
	seen := map[string]bool{}
	out := dirs[:0]
	for _, d := range dirs {
		if !seen[d] && !strings.ContainsRune(d, ':') {
			seen[d] = true
			out = append(out, d)
		}
	}
	return strings.Join(out, ":")
}

// systemdEscape protects the characters systemd expands or ends a quoted
// value on.
func systemdEscape(s string) string {
	return strings.NewReplacer("%", "%%", `"`, `\"`, `\`, `\\`).Replace(s)
}

var (
	systemdPathRE = regexp.MustCompile(`(?m)^Environment="?PATH=([^"\n]*)"?$`)
	plistPathRE   = regexp.MustCompile(`(?s)<key>PATH</key>\s*<string>([^<]*)</string>`)
)

// installedServicePath returns the PATH of the installed daemon unit, or ""
// when it is not installed (or has no PATH).
func (e *Env) installedServicePath() string {
	switch e.GOOS {
	case "linux":
		b, err := os.ReadFile(filepath.Join(e.Home, ".config", "systemd", "user", "agent-mesh-daemon.service"))
		if m := systemdPathRE.FindSubmatch(b); err == nil && m != nil {
			return strings.ReplaceAll(strings.ReplaceAll(string(m[1]), "%%", "%"), `\\`, `\`)
		}
	case "darwin":
		b, err := os.ReadFile(filepath.Join(e.Home, "Library", "LaunchAgents", "com.agentmesh.daemon.plist"))
		if m := plistPathRE.FindSubmatch(b); err == nil && m != nil {
			return string(m[1])
		}
	}
	return ""
}

// servicePathCheck warns when the daemon service cannot find the claude the
// interactive shell uses.
func servicePathCheck(e *Env) Result {
	const name = "service PATH"
	have := e.installedServicePath()
	if have == "" {
		return Result{name, Pass, "no installed daemon unit to check", ""}
	}
	bin, err := e.Runner.LookPath("claude")
	if err != nil {
		return Result{name, Pass, "claude not found (see claude check)", ""}
	}
	dir := filepath.Dir(bin)
	for _, d := range strings.Split(have, ":") {
		if d == dir {
			return Result{name, Pass, "daemon unit PATH includes " + dir, ""}
		}
	}
	return Result{name, Warn, "daemon unit PATH lacks " + dir + " (where claude lives); workers cannot start it",
		"re-run agent-mesh up (hub machine) or agent-mesh join again from a shell where claude works; it rewrites the unit PATH"}
}
