package hub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// PidFileName is written next to hub.toml while the hub runs, so tools can
// send SIGHUP when no service manager is involved.
const PidFileName = "hub.pid"

// replace atomically swaps the token tables with those of next.
func (a *authenticator) replace(next *authenticator) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.admin, a.hosts, a.sessions = next.admin, next.hosts, next.sessions
}

// Reload re-reads the token tables (admin hash, host and session tokens) from
// cfg. Listen address, database and timings need a restart.
func (s *Server) Reload(cfg Config) { s.auth.replace(newAuthenticator(cfg)) }

// watchReload applies hub.toml token changes on each signal from hup (already
// registered for SIGHUP by the caller) and records a pidfile next to it. A config that fails to load or validate is logged and ignored,
// so a bad edit never locks everyone out. The returned func stops watching
// and removes the pidfile.
func (s *Server) watchReload(ctx context.Context, path string, hup <-chan os.Signal) (stop func()) {
	pidPath := filepath.Join(filepath.Dir(path), PidFileName)
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		s.log.Warn("write pidfile", "path", pidPath, "err", err)
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-hup:
				cfg, err := LoadConfig(path)
				if err != nil {
					s.log.Error("reload rejected, keeping current tokens", "err", err)
					continue
				}
				s.Reload(cfg)
				s.log.Info("reloaded tokens", "hosts", len(cfg.HostTokens), "sessions", len(cfg.SessionTokens))
			}
		}
	}()
	return func() {
		close(done)
		_ = os.Remove(pidPath)
	}
}

// SignalReload sends SIGHUP to the hub whose pidfile is at pidPath, after
// verifying the pid still belongs to an agent-mesh hub process (a stale
// pidfile may name an unrelated process).
func SignalReload(pidPath string) error {
	b, err := os.ReadFile(pidPath)
	if err != nil {
		return fmt.Errorf("read hub pidfile: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return fmt.Errorf("invalid pid in %s", pidPath)
	}
	if err := checkHubProcess(pid); err != nil {
		return fmt.Errorf("%w (stale %s? remove it and restart the hub)", err, pidPath)
	}
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		return fmt.Errorf("signal hub pid %d: %w", pid, err)
	}
	return nil
}
