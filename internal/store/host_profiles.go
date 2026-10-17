package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/felixn678/agent-mesh/internal/api"
)

// ErrHostNoteOwner: a session token may only set its own machine's note.
var ErrHostNoteOwner = errors.New("a session token may only set its own machine's note; use the admin token (hub machine) for other machines")

// upsertMachine records the OS and tools a session-start hook detected on
// host, keeping the user's note.
func (s *Store) upsertMachine(ctx context.Context, host string, m api.MachineInfo) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_profiles (name, os, tools, updated_at) VALUES (?,?,?,?)
	  ON CONFLICT(name) DO UPDATE SET os=excluded.os, tools=excluded.tools, updated_at=excluded.updated_at`,
		host, m.OS, strings.Join(m.Tools, ","), s.ms())
	if err != nil {
		return fmt.Errorf("upsert host profile: %w", err)
	}
	return nil
}

// SetHostNote sets (empty clears) the role note of machine name. ownerHost ""
// is the admin; otherwise it must be name.
func (s *Store) SetHostNote(ctx context.Context, ownerHost, name string, r api.HostNoteRequest) error {
	if !api.ValidHostName(name) {
		return &api.ValidationError{Msg: "invalid host name"}
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if ownerHost != "" && ownerHost != name {
		return ErrHostNoteOwner
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_profiles (name, note, updated_at) VALUES (?,?,?)
	  ON CONFLICT(name) DO UPDATE SET note=excluded.note, updated_at=excluded.updated_at`,
		name, strings.TrimSpace(r.Note), s.ms())
	if err != nil {
		return fmt.Errorf("set host note: %w", err)
	}
	return nil
}

// ListHostProfiles returns every machine the hub knows of (a profile, a
// daemon load report or a live session) with its live session count.
func (s *Store) ListHostProfiles(ctx context.Context) ([]api.HostProfile, error) {
	rows, err := s.db.QueryContext(ctx, `WITH names AS (
	    SELECT name FROM host_profiles UNION SELECT name FROM hosts
	    UNION SELECT host FROM sessions WHERE ended_at IS NULL AND last_seen_at >= ?1)
	  SELECT n.name, COALESCE(p.note,''), COALESCE(p.os,''), COALESCE(p.tools,''), COALESCE(p.updated_at,0),
	    (SELECT COUNT(*) FROM sessions x WHERE x.host = n.name AND x.ended_at IS NULL AND x.last_seen_at >= ?1)
	  FROM names n LEFT JOIN host_profiles p ON p.name = n.name ORDER BY n.name`, s.aliveCutoff())
	if err != nil {
		return nil, fmt.Errorf("list host profiles: %w", err)
	}
	defer rows.Close()
	out := []api.HostProfile{}
	for rows.Next() {
		var h api.HostProfile
		var tools string
		if err := rows.Scan(&h.Name, &h.Note, &h.OS, &tools, &h.UpdatedAt, &h.LiveSessions); err != nil {
			return nil, fmt.Errorf("scan host profile: %w", err)
		}
		if tools != "" {
			h.Tools = strings.Split(tools, ",")
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
