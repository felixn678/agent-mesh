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
	"time"
)

// launchdManager manages LaunchAgents labelled com.agentmesh.<name>.
type launchdManager struct{ e *Env }

func (m *launchdManager) label(name string) string { return "com.agentmesh." + name }

func (m *launchdManager) target(name string) string { return m.domain() + "/" + m.label(name) }

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
// launchd never rotates StandardOutPath, so the process writes its own
// size-capped log (--log-file, 10 MB x 3) and stdout/stderr only catch
// startup failures (trimmed by the process when they grow past 1 MB).
func renderPlist(home, bin, path string, u Unit, label string) string {
	logDir := filepath.Join(home, "Library", "Logs", "agent-mesh")
	var args strings.Builder
	for _, a := range append(append([]string{bin}, u.Args...), "--log-file", filepath.Join(logDir, u.Name+".log")) {
		fmt.Fprintf(&args, "    <string>%s</string>\n", html.EscapeString(a))
	}
	log := html.EscapeString(filepath.Join(logDir, u.Name+".stderr.log"))
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
`, label, args.String(), html.EscapeString(path), log, log)
}

// launchdRetryDelay spaces bootstrap retries: launchd finishes a bootout
// asynchronously, and bootstrapping too early fails with "5: Input/output
// error". A variable so tests run without sleeping.
var launchdRetryDelay = 300 * time.Millisecond

const launchdBootstrapAttempts = 10

// domain is the per-user GUI launchd domain LaunchAgents live in.
func (m *launchdManager) domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

// Install writes the plist and makes launchd run it. Cases:
//   - not loaded: bootstrap it;
//   - loaded, unchanged: start it if it is not running (kickstart), else no-op;
//   - loaded, changed: bootout, then bootstrap the new definition (retried
//     while launchd is still tearing the old one down).
func (m *launchdManager) Install(ctx context.Context, u Unit) error {
	// The gui domain only exists while the user is logged in to the Mac (not
	// over a plain SSH session); fail with a clear hint instead of launchctl's
	// numeric error.
	if _, err := m.e.Runner.Output(ctx, "launchctl", "print", m.domain()); err != nil {
		return fmt.Errorf("launchd %s domain is unavailable (no desktop login for this user, e.g. only SSH): log in on the Mac once, or re-run with --no-service", m.domain())
	}
	if err := os.MkdirAll(filepath.Join(m.e.Home, "Library", "Logs", "agent-mesh"), 0o700); err != nil {
		return err
	}
	path := m.plistPath(u.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	want := []byte(renderPlist(m.e.Home, m.e.Binary, m.e.servicePath(), u, m.label(u.Name)))
	cur, _ := os.ReadFile(path)
	changed := !bytes.Equal(cur, want)
	if changed {
		if err := writeAtomic(path, want, 0o644); err != nil {
			return err
		}
	}
	out, err := m.e.Runner.Output(ctx, "launchctl", "print", m.target(u.Name))
	loaded := err == nil
	switch {
	case loaded && !changed && bytes.Contains(out, []byte("state = running")):
		return nil
	case loaded && !changed:
		return m.ctl(ctx, "kickstart", m.target(u.Name))
	case loaded:
		if err := m.ctl(ctx, "bootout", m.target(u.Name)); err != nil {
			return err
		}
	}
	return m.bootstrap(ctx, path)
}

// bootstrap loads the plist into the gui domain, retrying while a previous
// instance is still being removed.
func (m *launchdManager) bootstrap(ctx context.Context, path string) error {
	var err error
	for i := 0; i < launchdBootstrapAttempts; i++ {
		if err = m.ctl(ctx, "bootstrap", m.domain(), path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(launchdRetryDelay):
		}
	}
	return err
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
