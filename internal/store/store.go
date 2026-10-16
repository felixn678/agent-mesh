package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

// Sentinel errors mapped to HTTP statuses by the hub.
var (
	ErrNotFound  = errors.New("task not found")
	ErrLeaseLost = errors.New("lease lost: fence or host no longer holds this task")
	ErrCancelled = errors.New("task was cancelled")
	ErrConflict  = errors.New("operation not allowed in the task's current state")
)

// LeaseConfig tunes lease timing.
type LeaseConfig struct {
	TTL          time.Duration // lease length granted by claim/heartbeat
	StaleRequeue time.Duration // expired this long below attempt cap -> requeue with fence+1
	ReviveWindow time.Duration // an expired, unreclaimed lease may still heartbeat this long
	Retention    time.Duration // ended/dead sessions and read messages are pruned after this (0 = 7 days)
	SessionAlive time.Duration // a session unseen this long is not alive (0 = api.SessionAliveWindow)
}

func (c LeaseConfig) sessionAlive() time.Duration {
	if c.SessionAlive <= 0 {
		return api.SessionAliveWindow
	}
	return c.SessionAlive
}

func (c LeaseConfig) retention() time.Duration {
	if c.Retention <= 0 {
		return 7 * 24 * time.Hour
	}
	return c.Retention
}

// DefaultLeaseConfig returns the production timings.
func DefaultLeaseConfig() LeaseConfig {
	return LeaseConfig{TTL: 5 * time.Minute, StaleRequeue: time.Hour, ReviveWindow: 30 * time.Minute}
}

// Store wraps the database with the queue operations. Time comes only from
// the injected clock so tests control expiry.
type Store struct {
	db     *sql.DB
	now    func() time.Time
	cfg    LeaseConfig
	notify *notifier
}

// New builds a Store. now==nil uses the wall clock.
func New(db *sql.DB, now func() time.Time, cfg LeaseConfig) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now, cfg: cfg, notify: newNotifier()}
}

// Now returns the store clock.
func (s *Store) Now() time.Time { return s.now() }

// Config returns the lease timings in use.
func (s *Store) Config() LeaseConfig { return s.cfg }

func (s *Store) ms() int64 { return s.now().UnixMilli() }
