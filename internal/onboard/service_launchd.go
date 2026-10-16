package onboard

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// launchdManager manages LaunchAgents labelled com.agentmesh.<name>.
type launchdManager struct{ e *Env }

func (m *launchdManager) label(name string) string { return "com.agentmesh." + name }

func (m *launchdManager) target(name string) string {
	return "gui/" + strconv.Itoa(os.Getuid()) + "/" + m.label(name)
}

func (m *launchdManager) plistPath(name string) string {
	return filepath.Join(m.e.Home, "Library", "LaunchAgents", m.label(name)+".plist")
}

func (m *launchdManager) ctl(ctx context.Context, args ...string) error {
	out, err := m.e.Runner.Output(ctx, "launchctl", args...)
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}

// renderPlist mirrors deploy/launchd: keep alive, private umask (63 = 0077).
func renderPlist(home, bin string, u Unit, label string) string {
	var args strings.Builder
	for _, a := range append([]string{bin}, u.Args...) {
		fmt.Fprintf(&args, "    <string>%s</string>\n", html.EscapeString(a))
	}
	log := html.EscapeString(filepath.Join(home, "Library", "Logs", "agent-mesh", u.Name+".log"))
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>%s</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>10</integer>
  <key>Umask</key>
  <integer>63</integer>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, label, args.String(), html.EscapeString(home+"/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"), log, log)
}

func (m *launchdManager) Install(ctx context.Context, u Unit) error {
	if err := os.MkdirAll(filepath.Join(m.e.Home, "Library", "Logs", "agent-mesh"), 0o700); err != nil {
		return err
	}
	path := m.plistPath(u.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	want := []byte(renderPlist(m.e.Home, m.e.Binary, u, m.label(u.Name)))
	cur, _ := os.ReadFile(path)
	changed := !bytes.Equal(cur, want)
	running, _ := m.Running(ctx, u.Name)
	if changed {
		if err := writeAtomic(path, want, 0o644); err != nil {
			return err
		}
	}
	if running && !changed {
		return nil
	}
	if running { // definition changed: reload it
		_ = m.ctl(ctx, "bootout", m.target(u.Name))
	}
	return m.ctl(ctx, "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), path)
}

func (m *launchdManager) Restart(ctx context.Context, name string) error {
	return m.ctl(ctx, "kickstart", "-k", m.target(name))
}

func (m *launchdManager) Reload(ctx context.Context, name string) error {
	return m.ctl(ctx, "kill", "SIGHUP", m.target(name))
}

func (m *launchdManager) Running(ctx context.Context, name string) (bool, error) {
	out, err := m.e.Runner.Output(ctx, "launchctl", "print", m.target(name))
	return err == nil && bytes.Contains(out, []byte("state = running")), nil
}
