package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
)

// runOutcome is how a claude run ended, from the daemon's point of view.
type runOutcome int

const (
	outOK        runOutcome = iota // result line, not an error
	outError                       // result line with is_error (budget, refusal, ...)
	outNoResult                    // exited without a result line (infra failure)
	outLeaseLost                   // hub said lease_lost/cancelled; killed
	outTimeout                     // task_timeout wall clock; killed
	outShutdown                    // daemon stopping; killed
)

// runRecord lets a restarted daemon find and kill an orphaned claude.
type runRecord struct {
	ID        int64  `json:"id"`
	Fence     int64  `json:"fence"`
	Repo      string `json:"repo"`
	Worktree  string `json:"worktree"`
	Branch    string `json:"branch"`
	PID       int    `json:"pid"`
	StartedAt int64  `json:"started_at"`
}

// runClaude runs one isolated `claude -p` in the worktree, in its own process
// group, heartbeating until it exits or must be killed.
func (d *Daemon) runClaude(ctx context.Context, t api.ClaimedTask, n taskNames) (Result, runOutcome, string, error) {
	if err := writePrivate(n.prompt, []byte(t.Prompt)); err != nil {
		return Result{}, outNoResult, "", err
	}
	defer os.Remove(n.prompt)
	stdin, err := os.Open(n.prompt)
	if err != nil {
		return Result{}, outNoResult, "", err
	}
	defer stdin.Close()
	stdout, err := os.OpenFile(n.log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return Result{}, outNoResult, "", err
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(n.errLog, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return Result{}, outNoResult, "", err
	}
	defer stderr.Close()

	rec := runRecord{ID: t.ID, Fence: t.Fence, Repo: t.Repo, Worktree: n.worktree, Branch: n.branch}
	if err := writeJSONFile(n.runRec, rec); err != nil {
		return Result{}, outNoResult, "", err
	}
	cmd := exec.Command(d.cfg.ClaudeBin, claudeArgs(d.cfg, t)...)
	cmd.Dir, cmd.Env = n.worktree, d.claudeEnv
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return Result{}, outNoResult, "", err
	}
	pid := cmd.Process.Pid // == pgid (Setpgid)
	rec.PID, rec.StartedAt = pid, time.Now().UnixMilli()
	if err := writeJSONFile(n.runRec, rec); err != nil {
		d.log.Warn("run record update failed", "task", t.ID, "err", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	lost := make(chan struct{})
	go d.heartbeatLoop(hbCtx, t.ID, t.Fence, lost)
	timer := time.NewTimer(d.cfg.TaskTimeout)
	defer timer.Stop()

	outcome := outOK
	select {
	case <-done:
	case <-lost:
		outcome = outLeaseLost
	case <-timer.C:
		outcome = outTimeout
	case <-ctx.Done():
		outcome = outShutdown
	}
	if outcome != outOK {
		d.killGroup(pid, done)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL) // stray children left in the group
	hbCancel()
	errTail := readTail(n.errLog, 500)
	res, perr := parseResultFile(n.log)
	if outcome != outOK {
		return res, outcome, errTail, nil
	}
	switch {
	case perr != nil || !res.Found:
		return res, outNoResult, errTail, perr
	case res.IsError:
		return res, outError, errTail, nil
	}
	return res, outOK, errTail, nil
}

// killGroup sends TERM to the process group, then KILL after killGrace.
func (d *Daemon) killGroup(pgid int, done <-chan error) {
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(d.killGrace):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-done
	}
}

// heartbeatLoop extends the lease every heartbeat_interval of wall-clock
// time. It checks often and compares wall time (not the monotonic clock,
// which stops during suspend), so a resumed laptop heartbeats at once and
// learns about a lost lease before doing anything else.
func (d *Daemon) heartbeatLoop(ctx context.Context, id, fence int64, lost chan<- struct{}) {
	check := max(d.cfg.HeartbeatInterval/4, 10*time.Millisecond)
	tk := time.NewTicker(check)
	defer tk.Stop()
	last := time.Now().Round(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		if now := time.Now().Round(0); now.Sub(last) < d.cfg.HeartbeatInterval {
			continue
		}
		last = time.Now().Round(0)
		err := d.hub.Heartbeat(ctx, id, api.HeartbeatRequest{Fence: fence})
		if client.IsLeaseLost(err) {
			d.log.Warn("lease lost; stopping task", "task", id, "fence", fence, "err", err)
			close(lost)
			return
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			d.log.Warn("heartbeat failed; continuing", "task", id, "err", err)
		}
	}
}

// fenceCheck is a one-off heartbeat before a publish side effect. It returns
// lost=true on 409; network errors are retried until ctx ends.
func (d *Daemon) fenceCheck(ctx context.Context, id, fence int64) (lost bool, err error) {
	err = d.report(ctx, "heartbeat", func(ctx context.Context) error {
		return d.hub.Heartbeat(ctx, id, api.HeartbeatRequest{Fence: fence})
	})
	if client.IsLeaseLost(err) {
		return true, nil
	}
	return false, err
}

func readTail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return tail(string(b), n)
}
