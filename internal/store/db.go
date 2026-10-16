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
const schemaVersion = 1

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
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}
