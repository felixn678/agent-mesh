package e2e_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
)

// The scenarios TestTwoHostMesh runs in order against one mesh.

func targeted(t *testing.T, ms *mesh) {
	type woke struct {
		count int
		at    time.Time
		err   error
	}
	wakes := make(chan woke, 2)
	for _, s := range []string{"m-one", "m-two"} {
		go func() {
			r, err := ms.onB.InboxWaitClaimable(bg, s, false, 20*time.Second)
			wakes <- woke{r.ClaimableCount, time.Now(), err}
		}()
	}
	time.Sleep(300 * time.Millisecond) // both waits are parked on the hub
	added := time.Now()
	t1, t2 := addTask(t, ms.onA, "fix login", "mac"), addTask(t, ms.onA, "bump deps", "mac")
	for range 2 {
		w := <-wakes
		if w.err != nil || w.count < 1 || w.at.Sub(added) > 2*time.Second {
			t.Fatalf("mac session wake: %+v after %v", w, w.at.Sub(added))
		}
	}
	if _, err := ms.onA.SessionClaim(bg, "a-two"); !errors.Is(err, client.ErrNoTask) {
		t.Fatalf("ubuntu session claimed a mac-targeted task: %v", err)
	}
	noClaimable(t, ms.onB)

	want := map[int64]bool{t1.ID: true, t2.ID: true}
	for _, s := range []string{"m-one", "m-two"} {
		req := api.SessionClaimRequest{RequestID: s + "-claim-1"}
		c, err := ms.onB.SessionClaimWith(bg, s, req)
		if err != nil || !want[c.ID] {
			t.Fatalf("%s claim: %+v %v", s, c, err)
		}
		delete(want, c.ID)
		if again, err := ms.onB.SessionClaimWith(bg, s, req); err != nil || again != c {
			t.Fatalf("%s retried claim got %+v want %+v (%v)", s, again, c, err)
		}
		if _, err := ms.onB.SessionComplete(bg, s, c.ID, api.CompleteRequest{Fence: c.Fence, Summary: "done by " + s}); err != nil {
			t.Fatal(err)
		}
	}
	notices := drainNotices(t, ms.onA, 2)
	for _, n := range notices {
		if !strings.Contains(n.Body, " done ") || !strings.Contains(n.Body, "result: done by m-") {
			t.Errorf("notice %q", n.Body)
		}
	}
	if left, _ := ms.onA.Inbox(bg, "a-boss", false); len(left) != 0 {
		t.Fatalf("acked notices re-delivered: %v", left)
	}
}

func untargeted(t *testing.T, ms *mesh) {
	task := addTask(t, ms.onA, "write docs", "")
	noClaimable(t, ms.onB)
	type claim struct {
		c       *client.Client
		session string
		got     api.ClaimedTask
		err     error
	}
	contenders := []claim{{c: ms.onA, session: "a-boss"}, {c: ms.onA, session: "a-two"},
		{c: ms.onB, session: "m-one"}, {c: ms.onB, session: "m-two"}}
	var wg sync.WaitGroup
	for i := range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k := &contenders[i]
			k.got, k.err = k.c.SessionClaim(bg, k.session)
		}()
	}
	wg.Wait()
	var winners []claim
	for _, k := range contenders {
		switch {
		case k.err == nil:
			winners = append(winners, k)
		case !errors.Is(k.err, client.ErrNoTask):
			t.Fatalf("%s: %v", k.session, k.err)
		}
	}
	if len(winners) != 1 || winners[0].got.ID != task.ID {
		t.Fatalf("want exactly one claim of #%d, got %+v", task.ID, winners)
	}
	w := winners[0]
	if _, err := w.c.SessionComplete(bg, w.session, w.got.ID, api.CompleteRequest{Fence: w.got.Fence, Summary: "docs"}); err != nil {
		t.Fatal(err)
	}
	drainNotices(t, ms.onA, 1)
}

func messaging(t *testing.T, ms *mesh) {
	send := func(c *client.Client, from, to, body string) {
		t.Helper()
		if _, err := c.SendMessage(bg, api.SendMessageRequest{FromSession: from, ToSession: to, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	receive := func(c *client.Client, session, wantBody, wantHost string) {
		t.Helper()
		var first []api.Message
		within(t, 3*time.Second, "message to "+session, func() bool {
			first, _ = c.InboxWait(bg, session, false, time.Second)
			return len(first) > 0
		})
		if len(first) != 1 || first[0].Body != wantBody || first[0].FromHost != wantHost {
			t.Fatalf("%s got %+v", session, first)
		}
		// Not acked (e.g. the hook timed out): delivered again.
		again, err := c.Inbox(bg, session, false)
		if err != nil || len(again) != 1 || again[0].ID != first[0].ID {
			t.Fatalf("unacked message not re-delivered: %+v %v", again, err)
		}
		if n, err := c.AckMessages(bg, session, []int64{first[0].ID}); err != nil || n != 1 {
			t.Fatalf("ack: %d %v", n, err)
		}
		if left, _ := c.Inbox(bg, session, false); len(left) != 0 {
			t.Fatalf("acked message re-delivered: %+v", left)
		}
	}
	send(ms.onA, "a-two", "m-one", "ping from ubuntu")
	receive(ms.onB, "m-one", "ping from ubuntu", "ubuntu")
	send(ms.onB, "m-one", "a-two", "pong from mac")
	receive(ms.onA, "a-two", "pong from mac", "mac")
}

func abandoned(t *testing.T, ms *mesh) {
	task := addTask(t, ms.onA, "refactor", "")
	dead, err := ms.onB.SessionClaim(bg, "m-two") // then m-two stops heartbeating
	if err != nil || dead.ID != task.ID {
		t.Fatalf("%+v %v", dead, err)
	}
	if _, err := ms.onA.SessionClaim(bg, "a-two"); !errors.Is(err, client.ErrNoTask) {
		t.Fatalf("live lease stolen: %v", err)
	}
	var taken api.ClaimedTask
	within(t, 6*time.Second, "reclaim after the 2s session lease", func() bool {
		taken, err = ms.onA.SessionClaim(bg, "a-two")
		return err == nil
	})
	if taken.ID != task.ID || taken.Fence != dead.Fence+1 {
		t.Fatalf("reclaim got %+v, abandoned %+v", taken, dead)
	}
	if _, err := ms.onB.SessionComplete(bg, "m-two", dead.ID, api.CompleteRequest{Fence: dead.Fence}); !client.IsLeaseLost(err) {
		t.Fatalf("zombie complete: %v", err)
	}
	if _, err := ms.onA.SessionComplete(bg, "a-two", taken.ID, api.CompleteRequest{Fence: taken.Fence, Summary: "refactored"}); err != nil {
		t.Fatal(err)
	}
	got, err := ms.admin.GetTask(bg, task.ID)
	if err != nil || got.State != api.StateDone || got.ClaimedBy != "session:a-two" || got.Attempts != 2 {
		t.Fatalf("final task %+v %v", got, err)
	}
	drainNotices(t, ms.onA, 1)
}
