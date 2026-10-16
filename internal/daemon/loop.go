package daemon

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/version"
)

// Claim backoff while the hub is unreachable, and how long a task that
// failed the local re-check is skipped.
const (
	backoffMin = 5 * time.Second
	backoffMax = 5 * time.Minute
	skipFor    = 30 * time.Minute
	gcEvery    = 24 * time.Hour
)

// Loop is the daemon's life: recover (orphans, journals), GC, preflight,
// then tick until ctx ends; finally wait for tasks to kill/release.
func (d *Daemon) Loop(ctx context.Context) error {
	d.sweepOrphans(ctx)
	d.replayJournals(ctx)
	d.gc(ctx)
	d.checkHealth(ctx, true)
	lastGC := time.Now()
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			d.log.Info("stopping: waiting for tasks to release")
			d.wg.Wait()
			return nil
		case <-t.C:
		}
		if time.Since(lastGC) >= gcEvery {
			d.gc(ctx)
			lastGC = time.Now()
		}
		d.tick(ctx)
		t.Reset(d.cfg.PollInterval)
	}
}

// hostLoad assembles the per-tick report.
func (d *Daemon) hostLoad(s loadSample, running int, reason string) api.HostLoad {
	d.mu.Lock()
	cv := d.claudeVersion
	d.mu.Unlock()
	return api.HostLoad{Cores: d.hw.Cores, RAMGB: d.hw.RAMGB, CPUModel: d.hw.CPUModel,
		Slots: d.slots, SlotsOverridden: d.slotsOverridden, CPUPct: s.CPUPct, MemAvailPct: s.MemAvailPct,
		AgentWorktrees: s.Worktrees, Running: running, ThrottleReason: reason,
		ClaudeVersion: cv, AgentVersion: version.Version}
}

// tick: health → sample → report load → maybe claim one task.
func (d *Daemon) tick(ctx context.Context) {
	d.checkHealth(ctx, false)
	s, err := d.sample(d.dir("worktrees"))
	d.mu.Lock()
	running, unhealthy := len(d.running), d.unhealthy
	d.mu.Unlock()
	if err != nil && unhealthy == "" {
		unhealthy = "load sample: " + err.Error()
	}
	reason := throttleReason(unhealthy, running, d.slots, s, d.cfg)
	load := d.hostLoad(s, running, reason)
	if err := d.hub.ReportLoad(ctx, load); err != nil {
		d.log.Warn("load report failed", "err", err)
	}
	if reason != "" || time.Now().Before(d.nextClaim) || ctx.Err() != nil {
		return
	}
	req := d.cfg.caps()
	req.Load = &load
	if d.claimID == "" {
		d.claimID = newRequestID()
	}
	req.RequestID = d.claimID
	task, err := d.hub.Claim(ctx, req)
	if !retryableClaimErr(err) {
		d.claimID = "" // the hub answered: the next claim is a new attempt
	}
	switch {
	case errors.Is(err, client.ErrNoTask):
		d.backoff = 0
		return
	case err != nil:
		d.backoff = min(max(d.backoff*2, backoffMin), backoffMax)
		jitter := time.Duration(rand.Int64N(int64(d.backoff / 4)))
		d.nextClaim = time.Now().Add(d.backoff + jitter)
		d.log.Warn("claim failed; backing off", "err", err, "backoff", d.backoff)
		return
	}
	d.backoff = 0
	if err := d.recheck(task); err != nil {
		d.log.Warn("claimed task does not fit this host; releasing", "task", task.ID, "err", err)
		_ = d.release(ctx, task, "host recheck: "+err.Error())
		return
	}
	d.log.Info("claimed task", "task", task.ID, "fence", task.Fence, "repo", task.Repo)
	d.startTask(ctx, task)
}

// newRequestID makes a claim attempt id; a retry after a lost response
// reuses it so the hub hands back the same task and fence.
func newRequestID() string {
	b := make([]byte, 12)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}

// retryableClaimErr: the claim may have landed without us seeing the answer.
func retryableClaimErr(err error) bool {
	var he *client.HTTPError
	if err == nil || errors.Is(err, client.ErrNoTask) || client.IsLeaseLost(err) {
		return false
	}
	return !errors.As(err, &he) || transientStatus(he.Status)
}

// recheck is the local defense-in-depth check after a claim; a misfit is
// skipped for skipFor so it cannot loop.
func (d *Daemon) recheck(t api.ClaimedTask) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if until, ok := d.skip[t.ID]; ok && now.Before(until) {
		d.nextClaim = now.Add(d.cfg.PollInterval) // it would be handed right back
		return errors.New("skipped after an earlier misfit")
	}
	if err := d.cfg.fits(t); err != nil {
		d.skip[t.ID] = now.Add(skipFor)
		return err
	}
	return nil
}
