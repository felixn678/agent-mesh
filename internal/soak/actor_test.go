//go:build soak

package soak

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
)

// actor simulates one interactive Claude session: it adds tasks, messages
// other sessions, waits like the background waiter, claims and works tasks,
// and may die (stop heartbeating) while holding one.
type actor struct {
	id, host, repo string
	c              *client.Client
	w              *world
	dead           atomic.Bool
	lastReg        time.Time
}

// retry repeats op while the hub is unreachable or restarting.
func (a *actor) retry(ctx context.Context, op string, f func() error) error {
	for i := 0; ; i++ {
		err := f()
		if !a.w.l.noteErr(op, err) || i >= 40 || ctx.Err() != nil {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (a *actor) register(ctx context.Context) error {
	a.lastReg = time.Now()
	return a.retry(ctx, "register", func() error {
		_, err := a.c.RegisterSession(ctx, api.RegisterSessionRequest{ID: a.id, Cwd: "/work/" + a.repo, Repo: a.repo})
		return err
	})
}

func (a *actor) loop(ctx context.Context) {
	for ctx.Err() == nil && !a.dead.Load() {
		if time.Since(a.lastReg) > 3*time.Second {
			_ = a.register(ctx)
		}
		if a.w.chaos.Load() {
			if rand.Float64() < 0.30 {
				a.addTask(ctx)
			}
			if rand.Float64() < 0.30 {
				a.send(ctx)
			}
		}
		a.waitOnce(ctx)
	}
}

func (a *actor) addTask(ctx context.Context) {
	spec := api.TaskSpec{Repo: a.repo, Prompt: "soak work from " + a.id, MaxBudgetUSD: 1, CreatedBySession: a.id}
	switch r := rand.IntN(3); r {
	case 0: // untargeted
	case 1:
		spec.TargetHost = a.w.hosts[rand.IntN(len(a.w.hosts))]
	case 2:
		peers := a.w.peers(a.repo)
		spec.TargetSession = peers[rand.IntN(len(peers))].id
	}
	var task api.Task
	err := a.retry(ctx, "add", func() (err error) { task, err = a.c.AddTask(ctx, spec); return err })
	if err == nil {
		a.w.l.mu.Lock()
		a.w.l.added[task.ID] = addRec{at: time.Now(), creator: a.id}
		a.w.l.mu.Unlock()
	}
}

func (a *actor) send(ctx context.Context) {
	to := a.w.all[rand.IntN(len(a.w.all))]
	body := fmt.Sprintf("hello from %s at %d", a.id, time.Now().UnixNano())
	var m api.Message
	err := a.retry(ctx, "send", func() (err error) {
		m, err = a.c.SendMessage(ctx, api.SendMessageRequest{FromSession: a.id, ToSession: to.id, Body: body})
		return err
	})
	if err == nil {
		a.w.l.mu.Lock()
		a.w.l.sent[m.ID] = to.id
		a.w.l.mu.Unlock()
	}
}

// waitOnce is one background-waiter round: fetch without marking read,
// ack what was shown, claim when work is claimable.
func (a *actor) waitOnce(ctx context.Context) {
	start := time.Now()
	resp, err := a.c.InboxWaitClaimable(ctx, a.id, false, 3*time.Second)
	if a.w.l.noteErr("wait", err) || err != nil {
		time.Sleep(200 * time.Millisecond)
		return
	}
	a.drain(ctx, resp.Messages)
	if resp.ClaimableCount > 0 {
		a.claimAndWork(ctx, start)
	}
}

func (a *actor) drain(ctx context.Context, msgs []api.Message) {
	if len(msgs) == 0 {
		return
	}
	ids := make([]int64, len(msgs))
	for i, m := range msgs {
		if m.ToSession != a.id {
			a.w.l.violate("session %s got message %d for %s", a.id, m.ID, m.ToSession)
		}
		ids[i] = m.ID
	}
	a.w.l.recordDelivery(ids)
	if err := a.retry(ctx, "ack", func() error { _, err := a.c.AckMessages(ctx, a.id, ids); return err }); err == nil {
		a.w.l.recordAck(ids)
	}
}

func (a *actor) claimAndWork(ctx context.Context, waitStart time.Time) {
	rid := fmt.Sprintf("%s-%d", a.id, rand.Int64())
	var t api.ClaimedTask
	var lat time.Duration
	err := a.retry(ctx, "claim", func() (err error) {
		s := time.Now()
		t, err = a.c.SessionClaimWith(ctx, a.id, api.SessionClaimRequest{RequestID: rid})
		lat = time.Since(s)
		return err
	})
	if err != nil {
		return
	}
	a.w.l.recordClaim(t.ID, t.Fence, a.id, lat, waitStart)
	if rand.Float64() < 0.10 { // simulate a lost claim response: retry with the same request id
		var again api.ClaimedTask
		err := a.retry(ctx, "claim-replay", func() (err error) {
			again, err = a.c.SessionClaimWith(ctx, a.id, api.SessionClaimRequest{RequestID: rid})
			return err
		})
		a.w.replays.Add(1)
		if err != nil || again.ID != t.ID || again.Fence != t.Fence {
			a.w.l.violate("claim replay %s: got task %d fence %d (%v), want %d/%d", rid, again.ID, again.Fence, err, t.ID, t.Fence)
		}
	}
	time.Sleep(time.Duration(50+rand.IntN(400)) * time.Millisecond) // "work"
	if rand.Float64() < a.w.deathRate && a.w.deaths.Load() < maxDeaths {
		a.die(t)
		return
	}
	_ = a.retry(ctx, "heartbeat", func() error {
		return a.c.SessionHeartbeat(ctx, a.id, t.ID, api.HeartbeatRequest{Fence: t.Fence})
	})
	a.finish(ctx, t, "finish")
}

// finish completes (85%), fails with retry (10%) or releases (5%) a task.
func (a *actor) finish(ctx context.Context, t api.ClaimedTask, op string) {
	r := rand.Float64()
	var applied bool
	err := a.retry(ctx, op, func() (err error) {
		switch {
		case r < 0.85:
			applied, err = a.c.SessionComplete(ctx, a.id, t.ID, api.CompleteRequest{Fence: t.Fence,
				Branch: fmt.Sprintf("agentmesh/task-%d-f%d", t.ID, t.Fence), Summary: "done by " + a.id})
		case r < 0.95:
			applied, err = a.c.SessionFail(ctx, a.id, t.ID, api.FailRequest{Fence: t.Fence, Retry: true, Reason: "soak fail"})
		default:
			applied, err = a.c.SessionRelease(ctx, a.id, t.ID, api.ReleaseRequest{Fence: t.Fence, Reason: "soak release"})
		}
		return err
	})
	if err == nil && !applied && r < 0.85 {
		a.w.l.recordComplete(t.ID, t.Fence)
	}
	if op == "late" {
		outcome := "accepted"
		switch {
		case client.IsLeaseLost(err):
			outcome = "lease_lost"
		case err != nil:
			outcome = "error"
		case applied:
			outcome = "replay"
		case r >= 0.85:
			outcome = "accepted_fail_or_release"
		}
		a.w.l.mu.Lock()
		a.w.l.late[outcome]++
		a.w.l.mu.Unlock()
	}
	var le *client.LeaseError
	if err != nil && !errors.As(err, &le) {
		a.w.l.violate("session %s %s task %d: unexpected error %v", a.id, op, t.ID, err)
	}
}

// die stops this session while it holds t. Half of the dead sessions come
// back after their lease expired and try a late complete.
func (a *actor) die(t api.ClaimedTask) {
	a.dead.Store(true)
	a.w.deaths.Add(1)
	if rand.IntN(2) == 0 {
		return
	}
	a.w.wg.Add(1)
	go func() {
		defer a.w.wg.Done()
		time.Sleep(time.Duration(10+rand.IntN(6)) * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		a.finish(ctx, t, "late")
	}()
}
