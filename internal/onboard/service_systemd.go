package onboard

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/felixn678/agent-mesh/internal/config"
)

// systemdManager manages systemd --user units named agent-mesh-<name>.service.
type systemdManager struct{ e *Env }

func (m *systemdManager) unitName(name string) string { return "agent-mesh-" + name + ".service" }

func (m *systemdManager) ctl(ctx context.Context, args ...string) ([]byte, error) {
	out, err := m.e.Runner.Output(ctx, "systemctl", append([]string{"--user"}, args...)...)
	if err != nil {
		return out, fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return out, nil
}

// renderSystemd mirrors deploy/systemd: restart on failure, private umask.
// cfgDir, when set, is passed on as AGENTMESH_CONFIG_DIR (see Env.ConfigDirOverride).
func renderSystemd(bin, path, cfgDir string, u Unit) string {
	after := ""
	extra := "TimeoutStopSec=60\nKillMode=mixed\n" // let the daemon release leases before workers die
	if u.Name == "daemon" {
		after = "After=agent-mesh-hub.service\n"
	} else {
		extra = ""
	}
	cfgEnv := ""
	if cfgDir != "" {
		cfgEnv = fmt.Sprintf("Environment=\"%s=%s\"\n", config.ConfigDirEnv, systemdEscape(cfgDir))
	}
	return fmt.Sprintf(`[Unit]
Description=%s
%sStartLimitIntervalSec=0

[Service]
ExecStart=%s %s
Environment="PATH=%s"
%sRestart=on-failure
RestartSec=10
UMask=0077
%s
[Install]
WantedBy=default.target
`, u.Desc, after, bin, strings.Join(u.Args, " "), systemdEscape(path), cfgEnv, extra)
}

func (m *systemdManager) Install(ctx context.Context, u Unit) error {
	if strings.ContainsAny(m.e.Binary, " \t\"'") {
		return fmt.Errorf("binary path %q contains spaces or quotes; move agent-mesh to a plain path", m.e.Binary)
	}
	dir := filepath.Join(m.e.Home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	path := filepath.Join(dir, m.unitName(u.Name))
	want := []byte(renderSystemd(m.e.Binary, m.e.servicePath(), m.e.ConfigDirOverride, u))
	cur, _ := os.ReadFile(path)
	changed := !bytes.Equal(cur, want)
	if changed {
		if err := writeAtomic(path, want, 0o644); err != nil {
			return err
		}
		if _, err := m.ctl(ctx, "daemon-reload"); err != nil {
			return err
		}
	}
	if _, err := m.ctl(ctx, "enable", "--now", m.unitName(u.Name)); err != nil {
		return err
	}
	if changed {
		_, err := m.ctl(ctx, "restart", m.unitName(u.Name))
		return err
	}
	return nil
}

func (m *systemdManager) Restart(ctx context.Context, name string) error {
	_, err := m.ctl(ctx, "restart", m.unitName(name))
	return err
}

func (m *systemdManager) Reload(ctx context.Context, name string) error {
	_, err := m.ctl(ctx, "kill", "--signal=SIGHUP", "--kill-whom=main", m.unitName(name))
	return err
}

func (m *systemdManager) Running(ctx context.Context, name string) (bool, error) {
	// is-active exits non-zero for inactive units; the printed state is the answer.
	out, _ := m.e.Runner.Output(ctx, "systemctl", "--user", "is-active", m.unitName(name))
	return strings.TrimSpace(string(out)) == "active", nil
}
