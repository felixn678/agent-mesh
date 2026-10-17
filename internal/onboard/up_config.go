package onboard

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/felixn678/agent-mesh/internal/config"
	"github.com/felixn678/agent-mesh/internal/hub"
)

// writeHubSide ensures the admin/host/session tokens and hub.toml. listen is
// the explicit --listen ("" when not given). It returns the effective listen
// address and whether an existing hub.toml's listen was changed.
func writeHubSide(e *Env, host, listen string) (eff string, changed bool, err error) {
	if err = ensureDir(e.ConfigDir); err != nil {
		return
	}
	if err = ensureDir(e.DataDir); err != nil {
		return
	}
	admin, _, err := ensureToken(e.Path("admin.token"))
	if err != nil {
		return "", false, err
	}
	hostTok, _, err := ensureToken(e.Path("host.token"))
	if err != nil {
		return "", false, err
	}
	sessTok, _, err := ensureToken(e.Path("session.token"))
	if err != nil {
		return "", false, err
	}
	hostHash, sessHash := config.HashToken(hostTok), config.HashToken(sessTok)
	path := e.Path("hub.toml")
	want := listen
	if want == "" {
		want = defaultListen
	}
	wrote, err := writeIfAbsent(path, []byte(hubTOML(want, config.HashToken(admin), host, hostHash, sessHash)))
	if err != nil {
		return "", false, err
	}
	if wrote {
		e.printf("created hub.toml (listen %s, host %q)\n", want, host)
		return want, false, nil
	}
	cfg, err := hub.LoadConfig(path)
	if err != nil {
		return "", false, fmt.Errorf("existing hub.toml is invalid, fix it first: %w", err)
	}
	eff = cfg.Listen
	_, haveHost := cfg.HostTokens[host]
	_, haveSess := cfg.SessionTokens[host]
	changed = listen != "" && listen != cfg.Listen
	if changed {
		eff = listen
	}
	if !changed && haveHost && haveSess {
		e.printf("kept existing hub.toml\n")
		return eff, false, nil
	}
	err = editHubTOML(path, func(src string) (string, error) {
		if changed {
			src, _ = setTopLevel(src, "listen", listen)
		}
		if !haveHost {
			src = addToTable(src, "host_tokens", fmt.Sprintf("%q = %q", host, hostHash))
		}
		if !haveSess {
			src = addToTable(src, "session_tokens", fmt.Sprintf("%q = %q", host, sessHash))
		}
		return src, nil
	})
	if err != nil {
		return "", false, err
	}
	e.printf("updated hub.toml (listen %s, host %q)\n", eff, host)
	return eff, changed, nil
}

// writeClientSide creates client.toml and daemon.toml when absent. With
// retarget (an explicit --listen) it also repoints their hub_url, since a hub
// bound to a Tailscale IP no longer answers on loopback.
func writeClientSide(e *Env, hubURL string, retarget bool) (daemonRetargeted bool, err error) {
	files := map[string]string{
		"client.toml": clientTOML(hubURL),
		"daemon.toml": daemonTOML(hubURL, e.Path("host.token"), e.DataDir),
	}
	for name, body := range files {
		path := e.Path(name)
		wrote, err := writeIfAbsent(path, []byte(body))
		if err != nil {
			return false, err
		}
		if wrote {
			e.printf("created %s\n", name)
			continue
		}
		if retarget {
			changed, err := setHubURL(path, hubURL)
			if err != nil {
				return false, err
			}
			daemonRetargeted = daemonRetargeted || (changed && name == "daemon.toml")
		}
	}
	return daemonRetargeted, nil
}

// setHubURL rewrites hub_url in an existing TOML file when it differs and
// reports whether it did.
func setHubURL(path, hubURL string) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	next, found := setTopLevel(string(src), "hub_url", hubURL)
	if !found || next == string(src) {
		return false, nil
	}
	return true, writeAtomic(path, []byte(next), 0o600)
}

// startServices installs and starts the hub and daemon user services. When
// the hub address changed, a running hub and daemon keep the old address in
// memory, so both are restarted (hub first, so the daemon finds it).
func startServices(ctx context.Context, e *Env, addressChanged bool) error {
	units := []Unit{
		{Name: "hub", Args: []string{"hub"}, Desc: "agent-mesh hub"},
		{Name: "daemon", Args: []string{"daemon"}, Desc: "agent-mesh host daemon (claude -p workers)"},
	}
	for _, u := range units {
		if err := e.Mgr.Install(ctx, u); err != nil {
			return fmt.Errorf("install %s service: %w", u.Name, err)
		}
		e.printf("service agent-mesh %s: installed and started\n", u.Name)
	}
	if addressChanged {
		for _, name := range []string{"hub", "daemon"} {
			if err := e.Mgr.Restart(ctx, name); err != nil {
				return fmt.Errorf("restart %s: %w", name, err)
			}
		}
	}
	if e.GOOS == "linux" {
		lingerHint(ctx, e)
	}
	return nil
}

// lingerHint prints the linger command when user services would stop at logout.
func lingerHint(ctx context.Context, e *Env) {
	u, err := user.Current()
	if err != nil {
		return
	}
	out, err := e.Runner.Output(ctx, "loginctl", "show-user", u.Username, "-p", "Linger")
	if err == nil && strings.Contains(string(out), "Linger=yes") {
		return
	}
	e.printf("hint: keep services running after logout and across reboots: loginctl enable-linger %s\n", u.Username)
}

// waitHealthy polls the hub's /healthz until it answers or timeout passes.
func waitHealthy(ctx context.Context, hubURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, hubURL+"/healthz", nil)
		if err != nil {
			return err
		}
		resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("healthz returned %d", resp.StatusCode)
		}
		last = err
		if time.Now().After(deadline) {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}
