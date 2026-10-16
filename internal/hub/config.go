// Package hub is the HTTP server: REST API, MCP mount, auth and the reaper.
package hub

import (
	"fmt"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/config"
	"github.com/felixn678/agent-mesh/internal/store"
)

// Config is hub.toml. Tokens appear only as sha256 hashes.
type Config struct {
	Listen         string            `toml:"listen"`
	DBPath         string            `toml:"db_path"`
	AdminTokenHash string            `toml:"admin_token_hash"`
	LeaseTTL       time.Duration     `toml:"lease_ttl"`
	ReapInterval   time.Duration     `toml:"reap_interval"`
	StaleRequeue   time.Duration     `toml:"stale_requeue"`
	ReviveWindow   time.Duration     `toml:"revive_window"`
	SessionAlive   time.Duration     `toml:"session_alive_window"`
	Retention      time.Duration     `toml:"retention"`
	HostTokens     map[string]string `toml:"host_tokens"`    // daemons: lease, load, reads
	SessionTokens  map[string]string `toml:"session_tokens"` // interactive sessions: messaging, task add, reads
}

// DefaultConfig returns the loopback-only defaults.
func DefaultConfig() Config {
	return Config{
		Listen:       "127.0.0.1:8787",
		DBPath:       "~/.local/share/agent-mesh/hub.db",
		LeaseTTL:     5 * time.Minute,
		ReapInterval: time.Minute,
		StaleRequeue: time.Hour,
		ReviveWindow: 30 * time.Minute,
		SessionAlive: api.SessionAliveWindow,
		Retention:    7 * 24 * time.Hour,
	}
}

// LoadConfig reads hub.toml over the defaults and validates it.
func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	if err := config.LoadTOML(path, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

// Validate checks hashes, host names and durations.
func (c Config) Validate() error {
	if _, err := config.ParseHash(c.AdminTokenHash); err != nil {
		return fmt.Errorf("admin_token_hash: %w", err)
	}
	seen := map[string]string{"admin_token_hash": c.AdminTokenHash}
	for table, tokens := range map[string]map[string]string{"host_tokens": c.HostTokens, "session_tokens": c.SessionTokens} {
		for name, h := range tokens {
			if !api.ValidHostName(name) || name == adminName {
				return fmt.Errorf("%s: invalid host name %q", table, name)
			}
			if _, err := config.ParseHash(h); err != nil {
				return fmt.Errorf("%s.%s: %w", table, name, err)
			}
			// One token must map to exactly one identity.
			if prev, dup := seen[h]; dup {
				return fmt.Errorf("%s.%s reuses the hash of %s", table, name, prev)
			}
			seen[h] = table + "." + name
		}
	}
	if c.Listen == "" || c.DBPath == "" {
		return fmt.Errorf("listen and db_path are required")
	}
	for name, d := range map[string]time.Duration{"lease_ttl": c.LeaseTTL, "reap_interval": c.ReapInterval,
		"stale_requeue": c.StaleRequeue, "revive_window": c.ReviveWindow,
		"session_alive_window": c.SessionAlive, "retention": c.Retention} {
		if d <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
	}
	return nil
}

// Lease converts the timings for store.New.
func (c Config) Lease() store.LeaseConfig {
	return store.LeaseConfig{TTL: c.LeaseTTL, StaleRequeue: c.StaleRequeue, ReviveWindow: c.ReviveWindow,
		SessionAlive: c.SessionAlive, Retention: c.Retention}
}
