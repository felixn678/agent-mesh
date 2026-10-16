//go:build soak

package soak

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
)

const (
	reposPerHost    = 3
	sessionsPerRepo = 5
	maxDeaths       = 8 // of 30 sessions; deaths are random until then
)

// daemonRepo has origins and daemon [repos] entries but no sessions.
var daemonRepo = fmt.Sprintf("https://github.com/example/soak-r%d.git", reposPerHost)

// world is the shared state of one soak run.
type world struct {
	l         *ledger
	hosts     []string
	all       []*actor
	chaos     atomic.Bool // adds and sends allowed
	deaths    atomic.Int64
	replays   atomic.Int64 // claims re-sent with the same request id
	deathRate float64
	wg        sync.WaitGroup
}

func (w *world) peers(repo string) []*actor {
	var out []*actor
	for _, a := range w.all {
		if a.repo == repo {
			out = append(out, a)
		}
	}
	return out
}

func soakDuration(t *testing.T) time.Duration {
	if v := os.Getenv("SOAK_DURATION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if testing.Short() {
		return 60 * time.Second
	}
	return 3 * time.Minute
}

type sample struct {
	label        string
	fds, threads int
}

func TestSoakChaos(t *testing.T) {
	dur := soakDuration(t)
	bin := buildBinary(t)
	hubM, other := newMachine(t, bin), newMachine(t, bin)
	addr := freePort(t)
	url := "http://" + addr
	hubM.run("up", "--no-service", "--listen", addr, "--name", "host-a")
	hubM.fastTimings()
	logDir := t.TempDir()
	openLog := func(name string) *os.File {
		f, err := os.OpenFile(filepath.Join(logDir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	logf := openLog("hub.log")
	h := &hubProc{m: hubM, url: url, log: logf}
	h.start()
	joinURL, raw, _ := invite(hubM, "host-b", url)
	other.run("join", joinURL, raw, "--no-service")
	admin := client.New(url, hubM.token("admin.token"))

	// Real daemons on both hosts with fake claude/gh, competing with sessions
	// for untargeted and host-targeted tasks.
	fakes, fakeDir, origins := fakeBin(t), t.TempDir(), makeOrigins(t)
	dlog := openLog("daemons.log")
	daemons := []*daemonProc{newDaemon(t, hubM, url, fakes, fakeDir, origins, dlog),
		newDaemon(t, other, url, fakes, fakeDir, origins, dlog)}
	for _, d := range daemons {
		d.start()
	}

	var samples []sample
	snap := func(label string) {
		f, th := procStats(h.pid())
		samples = append(samples, sample{label, f, th})
	}
	snap("start idle")

	w := &world{l: newLedger(), hosts: []string{"host-a", "host-b"}, deathRate: 0.02}
	for hi, m := range []*machine{hubM, other} {
		c := client.New(url, m.token("session.token"))
		for r := 0; r < reposPerHost; r++ {
			repo := fmt.Sprintf("https://github.com/example/soak-r%d.git", r)
			for s := 0; s < sessionsPerRepo; s++ {
				w.all = append(w.all, &actor{id: fmt.Sprintf("%s-r%d-s%d", w.hosts[hi], r, s),
					host: w.hosts[hi], repo: repo, c: c, w: w})
			}
		}
	}
	ctx, stopActors := context.WithCancel(context.Background())
	for _, a := range w.all {
		if err := a.register(ctx); err != nil {
			t.Fatal(err)
		}
	}
	w.chaos.Store(true)
	begin := time.Now()
	for _, a := range w.all {
		w.wg.Add(1)
		go func(a *actor) { defer w.wg.Done(); a.loop(ctx) }(a)
	}

	// Chaos schedule (fractions of the chaos phase): graceful hub restart,
	// two hub SIGKILL crashes, a token reload (SIGHUP via invite) and a
	// daemon SIGKILL while its claude runs.
	var reloadOK bool
	var kr killResult
	at := func(f float64) { time.Sleep(time.Until(begin.Add(time.Duration(float64(dur) * f)))) }
	steps := []struct {
		f    float64
		name string
		do   func()
	}{
		{0.20, "SIGTERM restart", func() { h.stop(); time.Sleep(time.Second); h.start() }},
		{0.35, "SIGKILL crash 1", func() { h.crash(); time.Sleep(time.Second); h.start() }},
		{0.45, "SIGHUP reload", func() { reloadOK = checkReload(t, hubM, url) }},
		{0.55, "daemon SIGKILL", func() { kr = killMidTask(t, daemons[1], admin) }},
		{0.75, "SIGKILL crash 2", func() { h.crash(); time.Sleep(time.Second); h.start() }},
	}
	for _, st := range steps {
		at(st.f)
		snap("before " + st.name)
		st.do()
	}
	at(1)
	snap("end of chaos (loaded)")
	w.chaos.Store(false)
	time.Sleep(30 * time.Second) // drain: expired leases reclaimed, inboxes emptied
	stopActors()
	w.wg.Wait()
	backlog := waitDaemonBacklog(t, admin, 150*time.Second)
	for _, d := range daemons {
		d.signal(syscall.SIGTERM)
	}
	finalDrain(t, w)
	time.Sleep(2 * time.Second)
	snap("end idle")
	elapsed := time.Since(begin)
	h.stop()

	r := checkInvariants(t, w, hubM.dbPath())
	r.logPath, r.samples, r.elapsed, r.reloadOK, r.kill = logf.Name(), samples, elapsed, reloadOK, kr
	r.hubErrors = grepErrors(t, logf.Name())
	r.daemonErrors = grepErrors(t, dlog.Name())
	r.graces = countLines(t, logf.Name(), "extended leases after restart")
	r.prs, r.openPRs = prChecks(t, fakeDir, r.prURL)
	r.print(t, w)
	t.Logf("daemon-repo backlog at end of drain: %d", backlog)
	if len(r.hubErrors) > 0 {
		t.Errorf("hub logged errors:\n%s", strings.Join(r.hubErrors, "\n"))
	}
	if !reloadOK {
		t.Error("token reload (SIGHUP) did not take effect")
	}
	if r.graces == 0 {
		t.Error("no startup grace logged after hub crashes")
	}
	if first, last := samples[0], samples[len(samples)-1]; last.fds > first.fds+10 {
		t.Errorf("hub FDs grew: %d at start idle, %d at end idle", first.fds, last.fds)
	}
}

// checkReload invites a third host mid-run (hub.toml rewrite + SIGHUP) and
// proves its brand-new session token works without a restart.
func checkReload(t *testing.T, hubM *machine, url string) bool {
	_, _, b := invite(hubM, "host-c", url)
	c := client.New(url, b.SessionToken)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		if _, err := c.RegisterSession(ctx, api.RegisterSessionRequest{ID: "host-c-probe"}); err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// finalDrain reads every live session's inbox to empty (fetch -> ack).
func finalDrain(t *testing.T, w *world) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, a := range w.all {
		if a.dead.Load() {
			continue
		}
		for ctx.Err() == nil {
			var msgs []api.Message
			err := a.retry(ctx, "final-inbox", func() (err error) { msgs, err = a.c.Inbox(ctx, a.id, false); return err })
			if err != nil {
				t.Errorf("final inbox %s: %v", a.id, err)
				break
			}
			if len(msgs) == 0 {
				break
			}
			a.drain(ctx, msgs)
		}
	}
}

// waitDaemonBacklog lets the daemons finish the daemon-only repo's queue
// (they are slower than sessions, especially under -race) before they stop.
func waitDaemonBacklog(t *testing.T, admin *client.Client, max time.Duration) int {
	deadline := time.Now().Add(max)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		n := 0
		for _, state := range []string{"queued", "running"} {
			ts, err := admin.ListTasks(ctx, state, time.Time{})
			if err != nil {
				t.Logf("backlog poll: %v", err)
			}
			for _, task := range ts {
				if task.Repo == canonical(daemonRepo) && task.Attempts < task.MaxAttempts {
					n++
				}
			}
		}
		cancel()
		if n == 0 || time.Now().After(deadline) {
			return n
		}
		time.Sleep(time.Second)
	}
}

func countLines(t *testing.T, path, sub string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), sub)
}

func grepErrors(t *testing.T, path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, " ERROR ") || strings.Contains(l, "level=ERROR") || strings.Contains(l, "panic") {
			out = append(out, l)
		}
	}
	return out
}
