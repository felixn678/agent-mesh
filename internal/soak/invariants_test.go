//go:build soak

package soak

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/store"
)

// result is what one run measured; print writes it to the test log.
type result struct {
	states                         map[string]int
	tasks, finished, notices       int
	runningHeldByStopped, stranded int
	messages, deliveredAlive       int
	logPath                        string
	samples                        []sample
	elapsed                        time.Duration
	reloadOK                       bool
	hubErrors                      []string
	queuedWhy                      map[string]int
}

var noticeRE = regexp.MustCompile(`^\[agent-mesh\] task #(\d+) (\w+) `)

type dbTask struct {
	state, reason, targetSession, repo, targetHost string
	fence                                          int64
	attempts, maxAttempts                          int
	leaseExp                                       sql.NullInt64
}

// checkInvariants compares the ledger with the hub database (hub stopped).
func checkInvariants(t *testing.T, w *world, dbPath string) *result {
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	l := w.l
	r := &result{states: map[string]int{}, queuedWhy: map[string]int{}}
	tasks := map[int64]dbTask{}
	rows, err := db.Query(`SELECT id, state, COALESCE(reason,''), COALESCE(target_session,''), repo,
	  COALESCE(target_host,''), fence, attempts, max_attempts, lease_expires_at FROM tasks`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		var d dbTask
		if err := rows.Scan(&id, &d.state, &d.reason, &d.targetSession, &d.repo, &d.targetHost,
			&d.fence, &d.attempts, &d.maxAttempts, &d.leaseExp); err != nil {
			t.Fatal(err)
		}
		tasks[id] = d
		r.states[d.state]++
	}
	rows.Close()
	r.tasks = len(tasks)

	alive := map[string]*actor{}
	for _, a := range w.all {
		if !a.dead.Load() {
			alive[a.id] = a
		}
	}
	now := time.Now().UnixMilli()
	for id := range l.added {
		d, ok := tasks[id]
		if !ok {
			t.Errorf("task %d acknowledged by task_add but missing from the hub", id)
			continue
		}
		switch d.state {
		case "done":
			r.finished++
		case "stuck":
			r.finished++
			if d.reason == "" {
				t.Errorf("task %d stuck without a reason", id)
			}
		case "running":
			if d.leaseExp.Valid && d.leaseExp.Int64 > now+60_000 {
				t.Errorf("task %d running with lease %v in the future (fence %d, attempts %d)", id,
					time.Duration(d.leaseExp.Int64-now)*time.Millisecond, d.fence, d.attempts)
			}
			r.runningHeldByStopped++
		case "queued":
			r.queuedWhy[queuedCause(d, alive)]++
			if eligibleTaker(d, w, alive) {
				r.stranded++
				t.Errorf("task %d queued at end although live session(s) could claim it (reason %q)", id, d.reason)
			}
		}
	}
	for id, fences := range l.completes {
		d := tasks[id]
		if len(fences) > 1 {
			t.Errorf("task %d completed more than once: fences %v", id, fences)
		}
		if d.state != "done" || d.fence != fences[0] {
			t.Errorf("task %d accepted complete at fence %d but hub has %s at fence %d", id, fences[0], d.state, d.fence)
		}
	}

	// Messages and creator notices.
	type dbMsg struct {
		from, to, body string
		read           bool
	}
	msgs := map[int64]dbMsg{}
	mrows, err := db.Query(`SELECT id, from_session, to_session, body, read_at IS NOT NULL FROM messages`)
	if err != nil {
		t.Fatal(err)
	}
	for mrows.Next() {
		var id int64
		var m dbMsg
		if err := mrows.Scan(&id, &m.from, &m.to, &m.body, &m.read); err != nil {
			t.Fatal(err)
		}
		msgs[id] = m
	}
	mrows.Close()
	for id, to := range l.sent {
		m, ok := msgs[id]
		r.messages++
		switch {
		case !ok:
			t.Errorf("message %d acknowledged by send but missing from the hub", id)
		case alive[to] != nil:
			r.deliveredAlive++
			if l.delivered[id] == 0 || !m.read {
				t.Errorf("message %d to live session %s: delivered %d times, read=%v", id, to, l.delivered[id], m.read)
			}
		}
	}
	seen := map[string]int{}
	for id, m := range msgs {
		if m.from != "hub" {
			continue
		}
		sub := noticeRE.FindStringSubmatch(m.body)
		if sub == nil {
			t.Errorf("unexpected hub message %d: %q", id, m.body)
			continue
		}
		r.notices++
		seen[sub[1]+" "+sub[2]]++
		if alive[m.to] != nil && !m.read {
			t.Errorf("notice %d to live creator %s never read", id, m.to)
		}
	}
	for key, n := range seen {
		if n > 1 {
			t.Errorf("notice %q sent %d times", key, n)
		}
	}
	for id := range l.added {
		d := tasks[id]
		if (d.state == "done" || d.state == "stuck") && seen[strconv.FormatInt(id, 10)+" "+d.state] == 0 {
			t.Errorf("finished task %d (%s) has no creator notice", id, d.state)
		}
	}
	for _, v := range l.violation {
		t.Errorf("live violation: %s", v)
	}
	if l.status500 > 0 {
		t.Errorf("hub answered 500 %d times", l.status500)
	}
	return r
}

// eligibleTaker: a live session could still claim d (same repo, host and
// session targeting allow it, attempts left).
func eligibleTaker(d dbTask, w *world, alive map[string]*actor) bool {
	if d.attempts >= d.maxAttempts {
		return false
	}
	for _, a := range alive {
		if canonical(a.repo) == d.repo && (d.targetHost == "" || d.targetHost == a.host) &&
			(d.targetSession == "" || d.targetSession == a.id) {
			return true
		}
	}
	return false
}

// queuedCause explains why a task is still queued at the end.
func queuedCause(d dbTask, alive map[string]*actor) string {
	switch {
	case d.attempts >= d.maxAttempts:
		return "attempts_at_cap"
	case d.targetSession != "" && alive[d.targetSession] == nil:
		return "target_session_dead"
	case d.targetSession != "":
		return "target_session_alive"
	case d.targetHost != "":
		return "target_host"
	}
	return "untargeted"
}

func canonical(remote string) string {
	s := strings.TrimPrefix(remote, "https://")
	return strings.TrimSuffix(s, ".git")
}

func (r *result) print(t *testing.T, w *world) {
	l := w.l
	var b strings.Builder
	fmt.Fprintf(&b, "\n=== soak result (%s wall, %d sessions, %d deaths) ===\n", r.elapsed.Round(time.Second), len(w.all), w.deaths.Load())
	fmt.Fprintf(&b, "tasks: %d added, %d in hub, states %v, finished %d (%.2f tasks/s)\n",
		len(l.added), r.tasks, r.states, r.finished, float64(r.finished)/r.elapsed.Seconds())
	fmt.Fprintf(&b, "end: %d running (held by stopped/dead sessions), %d stranded queued, queued by cause %v\n",
		r.runningHeldByStopped, r.stranded, r.queuedWhy)
	fmt.Fprintf(&b, "claims: %d (task,fence) pairs, %d same-request-id replays; accepted completes %d; late completes %v\n",
		len(l.claims), w.replays.Load(), len(l.completes), l.late)
	fmt.Fprintf(&b, "latency: claim p50 %v p99 %v | add->claim p50 %v p99 %v | wake p50 %v p99 %v (n=%d)\n",
		pct(l.claimLat, .5), pct(l.claimLat, .99), pct(l.addToClaim, .5), pct(l.addToClaim, .99),
		pct(l.wakeLat, .5), pct(l.wakeLat, .99), len(l.wakeLat))
	fmt.Fprintf(&b, "messages: %d sent, %d to live sessions all delivered+read, %d acked; creator notices %d\n",
		r.messages, r.deliveredAlive, len(l.acked), r.notices)
	fmt.Fprintf(&b, "client errors: %v; HTTP 500s: %d; hub log errors: %d; reload ok: %v\n", l.errs, l.status500, len(r.hubErrors), r.reloadOK)
	for _, s := range r.samples {
		fmt.Fprintf(&b, "hub proc %-24s fds=%d threads=%d\n", s.label, s.fds, s.threads)
	}
	t.Log(b.String())
}
