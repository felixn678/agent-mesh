// Package store is the hub's SQLite persistence: tasks, fenced leases, hosts.
package store

import (
	"context"
	"database/sql"
	_ "embed" // schema.sql
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver, registers "sqlite"
)

//go:embed schema.sql
var schemaSQL string

// schemaVersion is stored in PRAGMA user_version. Future changes must be
// forward-only migrations keyed on it.
const schemaVersion = 5

// upgradeV1 adds the v2 columns to a v1 database. SQLite has no ADD COLUMN IF
// NOT EXISTS, so a concurrent first open may find a column already added.
var upgradeV1 = []string{
	"ALTER TABLE tasks ADD COLUMN target_host TEXT",
	"ALTER TABLE tasks ADD COLUMN target_session TEXT",
	"ALTER TABLE tasks ADD COLUMN created_by_session TEXT",
	"ALTER TABLE tasks ADD COLUMN summary TEXT",
	"ALTER TABLE sessions ADD COLUMN repo TEXT NOT NULL DEFAULT ''",
}

// upgradeV2 adds the v3 claim retry column.
var upgradeV2 = []string{"ALTER TABLE tasks ADD COLUMN claim_request_id TEXT"}

// upgradeV3 drops the v3 completion trigger; schemaSQL then recreates it with
// the v4 condition (CREATE TRIGGER IF NOT EXISTS cannot replace a trigger).
var upgradeV3 = []string{"DROP TRIGGER IF EXISTS tasks_notify_creator"}

// upgradeV4 adds the v5 task-add request id (its unique index is in schemaSQL).
var upgradeV4 = []string{"ALTER TABLE tasks ADD COLUMN request_id TEXT"}

// upgrades maps a stored version to the statements that lift it by one.
var upgrades = map[int][]string{1: upgradeV1, 2: upgradeV2, 3: upgradeV3, 4: upgradeV4}

// Open opens (creating if needed, mode 0600) the hub database and migrates it.
// synchronous=FULL: a fence that was returned to a worker must survive power
// loss. One connection per handle serialises writes inside a process;
// busy_timeout covers other processes/handles.
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create db file: %w", err)
	}
	_ = f.Close()

	esc := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
	dsn := "file:" + esc + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(FULL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	var v int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if v > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than this binary (%d)", v, schemaVersion)
	}
	if v == schemaVersion {
		return nil
	}
	for from := v; from >= 1 && from < schemaVersion; from++ { // v0 = fresh file: schemaSQL creates everything
		for _, q := range upgrades[from] {
			if _, err := db.ExecContext(ctx, q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("upgrade schema v%d: %w", from, err)
			}
		}
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}
