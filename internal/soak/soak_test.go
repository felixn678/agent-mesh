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
	logf, err := os.OpenFile(filepath.Join(t.TempDir(), "hub.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()
	h := &hubProc{m: hubM, url: url, log: logf}
	h.start()
	joinURL, raw, _ := invite(hubM, "host-b", url)
	other.run("join", joinURL, raw, "--no-service")

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

	// Chaos schedule: hub restarts at 25/50/75 %, token reload (SIGHUP via
	// invite) at 40 %.
	reloadOK := false
	at := func(f float64) { time.Sleep(time.Until(begin.Add(time.Duration(float64(dur) * f)))) }
	for i, f := range []float64{0.25, 0.4, 0.5, 0.75} {
		at(f)
		if i == 1 {
			reloadOK = checkReload(t, hubM, url)
			snap("after reload")
			continue
		}
		snap(fmt.Sprintf("before restart %d", i+1))
		h.stop()
		time.Sleep(time.Second)
		h.start()
	}
	at(1)
	snap("end of chaos (loaded)")
	w.chaos.Store(false)
	time.Sleep(30 * time.Second) // drain: expired leases reclaimed, inboxes emptied
	stopActors()
	w.wg.Wait()
	finalDrain(t, w)
	time.Sleep(2 * time.Second)
	snap("end idle")
	elapsed := time.Since(begin)
	h.stop()

	if b, err := os.ReadFile(filepath.Join(hubM.cfg, "hub.toml")); err == nil {
		t.Logf("final hub.toml timings:\n%s", grepLines(string(b), "ttl", "revive", "reap"))
	}
	r := checkInvariants(t, w, hubM.dbPath())
	r.logPath, r.samples, r.elapsed, r.reloadOK = logf.Name(), samples, elapsed, reloadOK
	r.hubErrors = grepErrors(t, logf.Name())
	r.print(t, w)
	if len(r.hubErrors) > 0 {
		t.Errorf("hub logged errors:\n%s", strings.Join(r.hubErrors, "\n"))
	}
	if !reloadOK {
		t.Error("token reload (SIGHUP) did not take effect")
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

func grepLines(s string, subs ...string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		for _, sub := range subs {
			if strings.Contains(l, sub) {
				out = append(out, l)
				break
			}
		}
	}
	return strings.Join(out, "\n")
}
