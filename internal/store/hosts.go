package store

import (
	"context"
	"fmt"

	"github.com/felixn678/agent-mesh/internal/api"
)

// UpsertHostLoad records a host's hardware, load and throttle reason. The name
// must come from the authenticated token, never from the request body.
func (s *Store) UpsertHostLoad(ctx context.Context, name string, l api.HostLoad) error {
	if !api.ValidHostName(name) {
		return &api.ValidationError{Msg: "invalid host name"}
	}
	if err := l.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO hosts
	  (name, cores, ram_gb, cpu_model, slots, slots_overridden, cpu_pct, mem_avail_pct,
	   agent_worktrees, running, throttle_reason, claude_version, agent_version, last_seen_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	  ON CONFLICT(name) DO UPDATE SET cores=excluded.cores, ram_gb=excluded.ram_gb,
	    cpu_model=excluded.cpu_model, slots=excluded.slots, slots_overridden=excluded.slots_overridden,
	    cpu_pct=excluded.cpu_pct, mem_avail_pct=excluded.mem_avail_pct,
	    agent_worktrees=excluded.agent_worktrees, running=excluded.running,
	    throttle_reason=excluded.throttle_reason, claude_version=excluded.claude_version,
	    agent_version=excluded.agent_version, last_seen_at=excluded.last_seen_at`,
		name, l.Cores, l.RAMGB, nullable(l.CPUModel), l.Slots, l.SlotsOverridden, l.CPUPct, l.MemAvailPct,
		l.AgentWorktrees, l.Running, nullable(l.ThrottleReason), nullable(l.ClaudeVersion),
		nullable(l.AgentVersion), s.ms())
	if err != nil {
		return fmt.Errorf("upsert host: %w", err)
	}
	return nil
}

// ListHosts returns all known hosts ordered by name.
func (s *Store) ListHosts(ctx context.Context) ([]api.Host, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, cores, ram_gb, COALESCE(cpu_model,''), slots,
	  slots_overridden, cpu_pct, mem_avail_pct, agent_worktrees, running, COALESCE(throttle_reason,''),
	  COALESCE(claude_version,''), COALESCE(agent_version,''), last_seen_at FROM hosts ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	defer rows.Close()
	out := []api.Host{}
	for rows.Next() {
		var h api.Host
		if err := rows.Scan(&h.Name, &h.Cores, &h.RAMGB, &h.CPUModel, &h.Slots, &h.SlotsOverridden,
			&h.CPUPct, &h.MemAvailPct, &h.AgentWorktrees, &h.Running, &h.ThrottleReason,
			&h.ClaudeVersion, &h.AgentVersion, &h.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan host: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
