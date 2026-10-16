package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	metaLastAlive = "last_alive_at"
	metaLastGrace = "last_grace_at"
)

// ReapResult counts what one reaper pass changed.
type ReapResult struct {
	Stuck                          int64 // expired at the attempt cap
	Requeued                       int64 // expired longer than StaleRequeue, below the cap
	TargetGone                     int64 // queued for a session that ended or went silent
	SessionsPruned, MessagesPruned int64
}

// Reap fixes abandoned leases and records last_alive_at. Leases that merely
// expired (below the cap, younger than StaleRequeue) are left alone: another
// claimer may take them via Claim, or the owner may revive them via Heartbeat.
func (s *Store) Reap(ctx context.Context) (ReapResult, error) {
	now := s.ms()
	var r ReapResult
	// At the cap nobody else may claim the task, so wait out ReviveWindow (the
	// owner may still heartbeat). lease_expires_at is kept: it marks the row as
	// reaper-stuck, which lets the owner still Complete or Fail it.
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET state='stuck',
	    reason='lease expired on '||claimed_by||' at attempt cap',
	    finished_at=:now, updated_at=:now
	  WHERE state='running' AND lease_expires_at < :now - :revive AND attempts >= max_attempts`,
		sql.Named("now", now), sql.Named("revive", s.cfg.ReviveWindow.Milliseconds()))
	if err != nil {
		return r, fmt.Errorf("reap at cap: %w", err)
	}
	r.Stuck, _ = res.RowsAffected()
	if r.Stuck > 0 {
		s.notify.signalAll() // creators of stuck tasks got a notice
	}

	if r.TargetGone, err = s.reapTargetGone(ctx, now); err != nil {
		return r, err
	}

	// fence+1 so a zombie worker's later heartbeat/complete gets lease_lost.
	res, err = s.db.ExecContext(ctx, `UPDATE tasks SET state='queued', fence=fence+1,
	    reason='lease expired on '||claimed_by||'; requeued', lease_expires_at=NULL, updated_at=:now
	  WHERE state='running' AND lease_expires_at < :now - :stale AND attempts < max_attempts`,
		sql.Named("now", now), sql.Named("stale", s.cfg.StaleRequeue.Milliseconds()))
	if err != nil {
		return r, fmt.Errorf("reap stale: %w", err)
	}
	r.Requeued, _ = res.RowsAffected()

	if r.SessionsPruned, r.MessagesPruned, err = s.pruneSessions(ctx, now); err != nil {
		return r, err
	}
	if err := s.setMeta(ctx, metaLastAlive, now); err != nil {
		return r, err
	}
	return r, nil
}

// reapTargetGone marks queued tasks stuck whose target session can no longer
// claim them: ended, unseen for the alive window, or restarted with worker
// mode off. A target that never registered (or was pruned) counts once the
// task itself is that old. The completion trigger notifies the creator; an
// admin may requeue.
func (s *Store) reapTargetGone(ctx context.Context, now int64) (int64, error) {
	cut := now - s.cfg.sessionAlive().Milliseconds()
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET state='stuck',
	    reason=CASE WHEN EXISTS (SELECT 1 FROM sessions s WHERE s.id = tasks.target_session AND s.worker = 0
	                             AND s.ended_at IS NULL AND s.last_seen_at >= :cut)
	           THEN 'target session does not take tasks' ELSE 'target session gone' END,
	    finished_at=:now, updated_at=:now
	  WHERE state='queued' AND target_session IS NOT NULL
	    AND NOT EXISTS (SELECT 1 FROM sessions s WHERE s.id = tasks.target_session
	                    AND s.ended_at IS NULL AND s.last_seen_at >= :cut AND s.worker = 1)
	    AND (EXISTS (SELECT 1 FROM sessions s WHERE s.id = tasks.target_session) OR tasks.created_at < :cut)`,
		sql.Named("now", now), sql.Named("cut", cut))
	if err != nil {
		return 0, fmt.Errorf("reap target gone: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.notify.signalAll() // creators got a notice
	}
	return n, nil
}

// Grace runs once after the hub is listening. It extends leases that were
// still alive when the previous process last ticked, so a restart does not
// strand running workers, and records last_grace_at. A crash loop graces only
// once because last_alive_at advances solely from Reap ticks. Returns the
// number of leases extended.
func (s *Store) Grace(ctx context.Context) (int64, error) {
	alive, okA, err := s.getMeta(ctx, metaLastAlive)
	if err != nil || !okA {
		return 0, err
	}
	graced, _, err := s.getMeta(ctx, metaLastGrace)
	if err != nil {
		return 0, err
	}
	if graced >= alive {
		return 0, nil
	}
	now := s.ms()
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET lease_expires_at = MAX(lease_expires_at, :now + :ttl)
	  WHERE state='running' AND lease_expires_at >= :alive`,
		sql.Named("now", now), sql.Named("ttl", s.cfg.TTL.Milliseconds()), sql.Named("alive", alive))
	if err != nil {
		return 0, fmt.Errorf("grace: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, s.setMeta(ctx, metaLastGrace, now)
}

func (s *Store) setMeta(ctx context.Context, key string, v int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, v)
	if err != nil {
		return fmt.Errorf("write meta %s: %w", key, err)
	}
	return nil
}

func (s *Store) getMeta(ctx context.Context, key string) (v int64, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read meta %s: %w", key, err)
	}
	return v, true, nil
}
