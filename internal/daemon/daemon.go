package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
)

// hubAPI is the subset of *client.Client the daemon uses (faked in tests).
type hubAPI interface {
	Claim(ctx context.Context, req api.ClaimRequest) (api.ClaimedTask, error)
	ReportLoad(ctx context.Context, l api.HostLoad) error
	Heartbeat(ctx context.Context, id int64, req api.HeartbeatRequest) error
	Complete(ctx context.Context, id int64, req api.CompleteRequest) (bool, error)
	Fail(ctx context.Context, id int64, req api.FailRequest) (bool, error)
	Release(ctx context.Context, id int64, req api.ReleaseRequest) (bool, error)
}

// Daemon is one host's pull worker. All state that must survive a crash
// lives in run records and journals under StateDir.
type Daemon struct {
	cfg             Config
	hub             hubAPI
	log             *slog.Logger
	hw              Hardware
	slots           int
	slotsOverridden bool
	sample          sampler
	claudeEnv       []string
	sshEnv          []string // ssh-agent vars, added for allow_ssh_agent profiles
	gitEnv          []string

	repoMu    sync.Mutex
	repoLocks map[string]*sync.Mutex

	mu            sync.Mutex
	running       map[int64]string // task id -> worktree path
	skip          map[int64]time.Time
	unhealthy     string
	infraFails    int
	lastPreflight time.Time
	claudeVersion string
	backoff       time.Duration
	nextClaim     time.Time
	claimID       string // request id of an unanswered claim, reused on retry

	wg sync.WaitGroup

	// Tunables and test seams.
	killGrace     time.Duration
	retryBase     time.Duration
	preflightFn   func(ctx context.Context) error
	stopAfterStep string // simulate a crash right after journaling this step
}

// New prepares state dirs (0700), env and hardware. hub is usually a
// *client.Client authenticated with this host's token.
func New(cfg Config, hub hubAPI, log *slog.Logger) (*Daemon, error) {
	if log == nil {
		log = slog.Default()
	}
	repos, err := canonicalRepos(cfg.Repos)
	if err != nil {
		return nil, err
	}
	cfg.Repos = repos
	claudeEnv, gitEnv, err := buildEnvs(os.Environ(), cfg.ClaudeEnvFile, cfg.GHEnvFile)
	if err != nil {
		return nil, err
	}
	for _, sub := range []string{"", "repos", "worktrees", "logs", "spool", "run"} {
		p := filepath.Join(cfg.StateDir, sub)
		if err := os.MkdirAll(p, 0o700); err != nil {
			return nil, fmt.Errorf("state dir: %w", err)
		}
		if err := os.Chmod(p, 0o700); err != nil {
			return nil, fmt.Errorf("state dir: %w", err)
		}
	}
	d := &Daemon{cfg: cfg, hub: hub, log: log, hw: detectHardware(), sample: sampleHost,
		claudeEnv: claudeEnv, sshEnv: pickEnv(os.Environ(), sshAgentEnv), gitEnv: gitEnv, repoLocks: map[string]*sync.Mutex{},
		running: map[int64]string{}, skip: map[int64]time.Time{},
		killGrace: 30 * time.Second, retryBase: 5 * time.Second}
	d.slots, d.slotsOverridden = slotsFor(cfg.MaxSlots, d.hw)
	d.preflightFn = d.preflight
	return d, nil
}

func (d *Daemon) dir(sub string) string { return filepath.Join(d.cfg.StateDir, sub) }

// taskNames are the per-attempt names; (id, fence) is unique forever.
type taskNames struct {
	branch, worktree, log, errLog, prompt, runRec, journal string
}

func (d *Daemon) names(id, fence int64) taskNames {
	base := fmt.Sprintf("task-%d-f%d", id, fence)
	return taskNames{
		branch:   "agentmesh/" + base,
		worktree: filepath.Join(d.dir("worktrees"), base),
		log:      filepath.Join(d.dir("logs"), base+".jsonl"),
		errLog:   filepath.Join(d.dir("logs"), base+".err"),
		prompt:   filepath.Join(d.dir("run"), base+".prompt"),
		runRec:   filepath.Join(d.dir("run"), base+".json"),
		journal:  filepath.Join(d.dir("spool"), base+".json"),
	}
}

// errStop means: give up on this report without retrying further.
var errStop = errors.New("stop")

// report retries a hub call on network errors with backoff until it lands,
// the hub answers definitively (lease error or other HTTP error) or ctx ends.
func (d *Daemon) report(ctx context.Context, what string, call func(ctx context.Context) error) error {
	delay := d.retryBase
	for {
		err := call(ctx)
		var he *client.HTTPError
		switch {
		case err == nil:
			return nil
		case client.IsLeaseLost(err):
			return err
		case errors.As(err, &he) && !transientStatus(he.Status):
			return err
		}
		d.log.Warn("hub call failed, retrying", "call", what, "err", err)
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(delay):
		}
		delay = min(delay*2, 5*time.Minute)
	}
}

// transientStatus: hub overloaded, restarting or cancelling long requests
// (503 unavailable) is retried like a network error.
func transientStatus(status int) bool { return status == 429 || status >= 500 }

// detached gives shutdown paths a short context of their own.
func detached() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

// release hands the task back; the error says it did not land (lease
// already gone, or the hub unreachable until ctx ended).
func (d *Daemon) release(ctx context.Context, t api.ClaimedTask, reason string) error {
	err := d.report(ctx, "release", func(ctx context.Context) error {
		_, err := d.hub.Release(ctx, t.ID, api.ReleaseRequest{Fence: t.Fence, Reason: short(reason)})
		return err
	})
	if err != nil {
		d.log.Warn("release not recorded", "task", t.ID, "err", err)
	}
	return err
}

// fail reports a failed attempt. It returns an error only when the report
// could not land because ctx ended (callers keep their journal for replay).
func (d *Daemon) fail(ctx context.Context, id, fence int64, retry bool, reason string, cost float64,
	wt, sessionID string) error {
	d.log.Info("task failed", "task", id, "fence", fence, "retry", retry, "reason", short(reason))
	err := d.report(ctx, "fail", func(ctx context.Context) error {
		_, err := d.hub.Fail(ctx, id, api.FailRequest{Fence: fence, Retry: retry, CostUSD: cost,
			Reason: short(reason), Worktree: wt, SessionID: sessionID})
		return err
	})
	if err != nil {
		d.log.Warn("fail not recorded", "task", id, "err", err)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

// infraFailure counts toward the circuit breaker; 2 in a row → unhealthy.
func (d *Daemon) infraFailure(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.infraFails++
	if d.infraFails >= 2 && d.unhealthy == "" {
		d.unhealthy = tail(redact(err.Error()), maxUnhealthyLen)
		d.lastPreflight = time.Now()
		d.log.Error("circuit breaker open", "err", d.unhealthy)
	}
}

func (d *Daemon) infraSuccess() {
	d.mu.Lock()
	d.infraFails = 0
	d.mu.Unlock()
}
