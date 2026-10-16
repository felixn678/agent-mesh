package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

func readPID(t *testing.T, fakeDir string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fakeDir, "claude.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

func assertDead(t *testing.T, fakeDir string) {
	t.Helper()
	pid := readPID(t, fakeDir)
	waitFor(t, "claude to exit", func() bool { return syscall.Kill(pid, 0) != nil })
}

func pidFileExists(fakeDir string) func() bool {
	return func() bool {
		_, err := os.Stat(filepath.Join(fakeDir, "claude.pid"))
		return err == nil
	}
}

func TestLeaseLostMidRunKillsClaudeWithoutPublishing(t *testing.T) {
	e := newEnv(t)
	d := e.daemon(nil)
	task := e.addTask("sleep")
	d.tick(t.Context())
	waitFor(t, "claude start", pidFileExists(e.fakeDir))
	e.hub.Clock.Advance(6 * time.Minute) // lease expired; another host reclaims
	if ct := e.claimAs("host-b"); ct.Fence != 2 {
		t.Fatalf("reclaim = %+v", ct)
	}
	waitTasks(t, d)
	assertDead(t, e.fakeDir)
	got := e.task(task.ID)
	if got.State != api.StateRunning || got.ClaimedBy != "host-b" {
		t.Fatalf("task = %+v", got)
	}
	if strings.Contains(e.remoteBranches(), "agentmesh/") || len(dirEntries(t, filepath.Join(e.cfg.StateDir, "worktrees"))) != 0 {
		t.Fatal("lost lease must not push and must clean its worktree")
	}
}

// flakyHub wraps the real client to inject faults around Complete.
type flakyHub struct {
	hubAPI
	mu             sync.Mutex
	beforeComplete func()
	dropFirst      bool
}

func (f *flakyHub) Complete(ctx context.Context, id int64, req api.CompleteRequest) (bool, error) {
	f.mu.Lock()
	before, drop := f.beforeComplete, f.dropFirst
	f.beforeComplete, f.dropFirst = nil, false
	f.mu.Unlock()
	if before != nil {
		before()
	}
	applied, err := f.hubAPI.Complete(ctx, id, req)
	if drop && err == nil {
		return false, errors.New("connection reset by peer") // response lost
	}
	return applied, err
}

func TestDroppedCompleteResponseKeepsPROpen(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("commit")
	runOne(t, e.daemon(&flakyHub{hubAPI: e.hub.Host("host-a"), dropFirst: true}))
	if got := e.task(task.ID); got.State != api.StateDone {
		t.Fatalf("task = %+v", got)
	}
	gh := e.ghState()
	if len(gh.PRs) != 1 || gh.PRs[0].State != "open" {
		t.Fatalf("replayed complete must not close the PR: %+v", gh.PRs)
	}
}

func TestCompleteLeaseLostClosesPRWithRetry(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("commit")
	if err := os.WriteFile(filepath.Join(e.fakeDir, "gh-close-fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fh := &flakyHub{hubAPI: e.hub.Host("host-a"), beforeComplete: func() {
		e.hub.Clock.Advance(6 * time.Minute)
		e.claimAs("host-b")
	}}
	runOne(t, e.daemon(fh))
	gh := e.ghState()
	if len(gh.PRs) != 1 || gh.PRs[0].State != "closed" {
		t.Fatalf("PR must be closed after lease loss: %+v", gh.PRs)
	}
	closes := 0
	for _, c := range gh.Calls {
		if c == "pr close" {
			closes++
		}
	}
	if closes != 2 {
		t.Fatalf("close attempts = %d; want a retry after the first failure", closes)
	}
	if got := e.task(task.ID); got.ClaimedBy != "host-b" {
		t.Fatalf("task = %+v", got)
	}
}

func TestCrashAfterPRCreatedReplaysWithoutSecondPR(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("commit")
	d := e.daemon(nil)
	d.stopAfterStep = stepPRCreated
	runOne(t, d)
	if len(dirEntries(t, filepath.Join(e.cfg.StateDir, "spool"))) != 1 {
		t.Fatal("journal should survive the crash")
	}
	d2 := e.daemon(nil)
	d2.sweepOrphans(t.Context())
	d2.replayJournals(t.Context())
	waitTasks(t, d2)
	if got := e.task(task.ID); got.State != api.StateDone || got.PRURL == "" {
		t.Fatalf("task = %+v", got)
	}
	if n := len(e.ghState().PRs); n != 1 {
		t.Fatalf("PRs = %d; want exactly one", n)
	}
	if left := dirEntries(t, filepath.Join(e.cfg.StateDir, "spool")); len(left) != 0 {
		t.Fatalf("journal left: %v", left)
	}
}

func TestCrashWhilePushingReplaysWithoutCollision(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("commit")
	d := e.daemon(nil)
	d.stopAfterStep = stepPushed // pushed, journal not advanced past it
	runOne(t, d)
	// Rewind the journal to "pushing" as if the crash hit right after git push.
	jp := d.names(task.ID, 1).journal
	var j journal
	if err := readJSONFile(jp, &j); err != nil {
		t.Fatal(err)
	}
	j.Step = stepPushing
	if err := writeJSONFile(jp, &j); err != nil {
		t.Fatal(err)
	}
	d2 := e.daemon(nil)
	d2.replayJournals(t.Context())
	waitTasks(t, d2)
	if got := e.task(task.ID); got.State != api.StateDone || got.PRURL == "" {
		t.Fatalf("task = %+v", got)
	}
}

func TestNewerFenceSupersedesOlderPR(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("commit")
	if ct := e.hostAClaim(); ct.Fence != 1 {
		t.Fatalf("claim = %+v", ct)
	}
	withGHState(e.fakeDir, func(s *ghState) {
		s.Next = 1
		s.PRs = append(s.PRs, fakePR{Number: 1, Head: "agentmesh/task-1-f1", Base: "main", State: "open",
			URL: "https://github.com/example/demo/pull/1"})
	})
	e.hub.Clock.Advance(6 * time.Minute)
	runOne(t, e.daemon(nil))
	if got := e.task(task.ID); got.State != api.StateDone || got.Branch != "agentmesh/task-1-f2" {
		t.Fatalf("task = %+v", got)
	}
	states := map[string]string{}
	for _, p := range e.ghState().PRs {
		states[p.Head] = p.State
	}
	if states["agentmesh/task-1-f1"] != "closed" || states["agentmesh/task-1-f2"] != "open" {
		t.Fatalf("PR states = %v", states)
	}
}

func TestHubDownMidRunStillCompletes(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("slowcommit")
	d := e.daemon(nil)
	d.tick(t.Context())
	time.Sleep(200 * time.Millisecond)
	e.hub.Stop()
	time.Sleep(2 * time.Second) // claude finishes while the hub is down
	e.hub.Restart()
	waitTasks(t, d)
	if got := e.task(task.ID); got.State != api.StateDone || got.PRURL == "" {
		t.Fatalf("task = %+v", got)
	}
}

func TestRestartKillsOrphanAndReleases(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("sleep")
	ct := e.hostAClaim()
	d := e.daemon(nil)
	// A claude left behind by a daemon that died mid-run.
	cmd := exec.Command(e.cfg.ClaudeBin, "-p")
	cmd.Stdin = strings.NewReader("sleep")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	n := d.names(ct.ID, ct.Fence)
	if err := writeJSONFile(n.runRec, runRecord{ID: ct.ID, Fence: ct.Fence, Repo: "demo",
		Worktree: n.worktree, Branch: n.branch, PID: cmd.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "orphan start", pidFileExists(e.fakeDir))
	d.sweepOrphans(t.Context())
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("orphan not killed")
	}
	if got := e.task(task.ID); got.State != api.StateQueued || got.Attempts != 0 {
		t.Fatalf("task = %+v; want released with attempts unchanged", got)
	}
	if _, err := os.Stat(n.runRec); !os.IsNotExist(err) {
		t.Fatal("run record left")
	}
}

func TestShutdownMidRunReleases(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("sleep")
	d := e.daemon(nil)
	ctx, cancel := context.WithCancel(t.Context())
	d.tick(ctx)
	waitFor(t, "claude start", pidFileExists(e.fakeDir))
	cancel()
	waitTasks(t, d)
	assertDead(t, e.fakeDir)
	if got := e.task(task.ID); got.State != api.StateQueued || got.Attempts != 0 {
		t.Fatalf("task = %+v", got)
	}
}

func TestBreakerOpensAfterTwoInfraFailures(t *testing.T) {
	e := newEnv(t)
	e.cfg.Repos["demo"] = filepath.Join(t.TempDir(), "missing.git")
	d := e.daemon(nil)
	for i := 0; i < 3; i++ {
		e.addTask("commit")
	}
	for i := 0; i < 3; i++ {
		runOne(t, d)
	}
	for id := int64(1); id <= 3; id++ {
		if got := e.task(id); got.State != api.StateQueued || got.Attempts != 0 {
			t.Fatalf("task %d = %+v", id, got)
		}
	}
	if got := e.task(3); got.Reason != "" {
		t.Fatalf("third task must never be claimed: %+v", got)
	}
	hosts, err := e.hub.Admin().Hosts(t.Context())
	if err != nil || len(hosts) != 1 || !strings.HasPrefix(hosts[0].ThrottleReason, "unhealthy: ") {
		t.Fatalf("hosts = %+v, %v", hosts, err)
	}
}

func TestReapedTaskStillRecordsFinishedWork(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("commit", func(s *api.TaskSpec) { s.MaxAttempts = 1 })
	d := e.daemon(nil)
	d.stopAfterStep = stepPrepare // claude finished; crash before publishing
	runOne(t, d)
	e.hub.Clock.Advance(40 * time.Minute) // past lease TTL + revive window
	if _, err := e.hub.Store.Reap(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := e.task(task.ID); got.State != api.StateStuck {
		t.Fatalf("reaper should mark stuck at the attempt cap: %+v", got)
	}
	d2 := e.daemon(nil)
	d2.replayJournals(t.Context())
	waitTasks(t, d2)
	got := e.task(task.ID)
	if got.State != api.StateStuck || !strings.HasPrefix(got.Reason, "lease lost before publish") ||
		got.SessionID != fakeSessionID || got.Worktree == "" {
		t.Fatalf("late fail not recorded: %+v", got)
	}
	if _, err := os.Stat(got.Worktree); err != nil || strings.Contains(e.remoteBranches(), "agentmesh/") {
		t.Fatalf("worktree must be kept and nothing pushed: %v", err)
	}
}

// lossyClaimHub drops the first claim response after the hub applied it.
type lossyClaimHub struct {
	hubAPI
	dropped bool
	ids     []string
}

func (h *lossyClaimHub) Claim(ctx context.Context, req api.ClaimRequest) (api.ClaimedTask, error) {
	h.ids = append(h.ids, req.RequestID)
	ct, err := h.hubAPI.Claim(ctx, req)
	if !h.dropped && err == nil {
		h.dropped = true
		return api.ClaimedTask{}, errors.New("i/o timeout")
	}
	return ct, err
}

func TestClaimRetryReusesRequestID(t *testing.T) {
	e := newEnv(t)
	task := e.addTask("commit")
	h := &lossyClaimHub{hubAPI: e.hub.Host("host-a")}
	d := e.daemon(h)
	runOne(t, d)
	d.nextClaim = time.Time{} // skip the backoff
	runOne(t, d)
	if len(h.ids) != 2 || h.ids[0] == "" || h.ids[0] != h.ids[1] {
		t.Fatalf("request ids = %v", h.ids)
	}
	if got := e.task(task.ID); got.State != api.StateDone || got.Fence != 1 || got.Attempts != 1 {
		t.Fatalf("retried claim must get the same attempt: %+v", got)
	}
}
