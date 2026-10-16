// Package hubtest runs a real hub (real SQLite file, real HTTP on loopback)
// with a fake clock for tests of the hub itself and of its clients.
package hubtest

import (
	"context"
	"database/sql"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/config"
	"github.com/felixn678/agent-mesh/internal/hub"
	"github.com/felixn678/agent-mesh/internal/store"
)

// AdminToken is the fixed admin token of every test hub.
const AdminToken = "test-admin-token"

// DefaultHosts are provisioned when Start is given no host names.
var DefaultHosts = []string{"host-a", "host-b"}

// Clock is a goroutine-safe fake clock shared with the hub's store.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// Now returns the current fake time.
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// Hub is a running test hub. Store and DB are replaced by Restart.
type Hub struct {
	URL        string
	Store      *store.Store
	DB         *sql.DB
	Clock      *Clock
	AdminToken string
	Config     hub.Config

	t      testing.TB
	dbPath string
	addr   string
	cancel context.CancelFunc
	done   chan error
	mu     sync.Mutex
}

// Start boots a hub on a random loopback port and stops it at test cleanup.
// hosts names the hosts to provision a host token and a session token for
// (default host-a, host-b).
func Start(t testing.TB, hosts ...string) *Hub {
	t.Helper()
	if len(hosts) == 0 {
		hosts = DefaultHosts
	}
	cfg := hub.DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.ReapInterval = time.Hour // tests call Store.Reap explicitly
	cfg.AdminTokenHash = config.HashToken(AdminToken)
	cfg.HostTokens, cfg.SessionTokens = map[string]string{}, map[string]string{}
	for _, name := range hosts {
		cfg.HostTokens[name] = config.HashToken(hostToken(name))
		cfg.SessionTokens[name] = config.HashToken(sessionToken(name))
	}
	h := &Hub{
		t: t, Config: cfg, AdminToken: AdminToken,
		Clock:  &Clock{t: time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)},
		dbPath: filepath.Join(t.TempDir(), "hub.db"),
	}
	cfg.DBPath = h.dbPath
	h.Config = cfg
	if err := h.boot("127.0.0.1:0"); err != nil {
		t.Fatalf("hubtest: start: %v", err)
	}
	t.Cleanup(h.Stop)
	return h
}

func hostToken(name string) string    { return "test-host-token-" + name }
func sessionToken(name string) string { return "test-session-token-" + name }

// SessionToken returns the session token provisioned for name (interactive
// Claude sessions on that host).
func (h *Hub) SessionToken(name string) string {
	h.t.Helper()
	if _, ok := h.Config.SessionTokens[name]; !ok {
		h.t.Fatalf("hubtest: host %q not provisioned", name)
	}
	return sessionToken(name)
}

// Session returns a client authenticated with the host's session token.
func (h *Hub) Session(name string) *client.Client { return client.New(h.URL, h.SessionToken(name)) }

// HostToken returns the token provisioned for name; it fails the test if the
// host was not passed to Start.
func (h *Hub) HostToken(name string) string {
	h.t.Helper()
	if _, ok := h.Config.HostTokens[name]; !ok {
		h.t.Fatalf("hubtest: host %q not provisioned", name)
	}
	return hostToken(name)
}

// Admin returns a client authenticated as admin.
func (h *Hub) Admin() *client.Client { return client.New(h.URL, h.AdminToken) }

// Host returns a client authenticated as the named host.
func (h *Hub) Host(name string) *client.Client { return client.New(h.URL, h.HostToken(name)) }

func (h *Hub) boot(addr string) error {
	db, err := store.Open(h.dbPath)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		_ = db.Close()
		return err
	}
	h.DB, h.addr, h.URL = db, ln.Addr().String(), "http://"+ln.Addr().String()
	h.Store = store.New(db, h.Clock.Now, h.Config.Lease())
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel, h.done = cancel, make(chan error, 1)
	srv := hub.NewServer(h.Config, h.Store)
	go func() { h.done <- srv.Serve(ctx, ln) }()
	return nil
}

// Stop shuts the hub down and closes its database. Safe to call twice.
func (h *Hub) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopLocked()
}

func (h *Hub) stopLocked() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	if err := <-h.done; err != nil {
		h.t.Errorf("hubtest: serve: %v", err)
	}
	_ = h.DB.Close()
	h.cancel = nil
}

// Restart simulates a hub process restart: stop, reopen the same database
// file, listen on the same address (so clients keep their URL) and run the
// startup grace again.
func (h *Hub) Restart() {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopLocked()
	if err := h.boot(h.addr); err != nil {
		h.t.Fatalf("hubtest: restart: %v", err)
	}
}
