package hub_test

import (
	"errors"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/hubtest"
)

func TestFetchThenAckOverREST(t *testing.T) {
	h := hubtest.Start(t)
	a, b := h.Session("host-a"), h.Session("host-b")
	regSession(t, a, "sa")
	regSession(t, b, "sb")
	if _, err := b.SendMessage(bg, api.SendMessageRequest{FromSession: "sb", ToSession: "sa", Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := a.Inbox(bg, "sa", false)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("%v %v", msgs, err)
	}
	if _, err := b.AckMessages(bg, "sa", []int64{msgs[0].ID}); httpStatus(err) != 403 {
		t.Fatalf("other host acked: %v", err)
	}
	if n, err := a.AckMessages(bg, "sa", []int64{msgs[0].ID}); err != nil || n != 1 {
		t.Fatalf("ack: %d %v", n, err)
	}
	if left, _ := a.Inbox(bg, "sa", false); len(left) != 0 {
		t.Fatalf("still unread: %v", left)
	}
}

func TestLongPollEndedByShutdownIsNotAServerError(t *testing.T) {
	h := hubtest.Start(t)
	s := h.Session("host-a")
	regSession(t, s, "sa")
	errc := make(chan error, 1)
	go func() {
		msgs, err := s.InboxWait(bg, "sa", false, 20*time.Second)
		if err == nil && len(msgs) != 0 {
			err = errors.New("unexpected messages")
		}
		errc <- err
	}()
	time.Sleep(300 * time.Millisecond) // let the wait start
	h.Stop()
	if err := <-errc; err != nil && httpStatus(err) != 503 {
		t.Fatalf("waiter on shutdown: %v", err) // previously a 500 internal error
	}
}

func TestClaimRetryWithRequestIDOverREST(t *testing.T) {
	h := hubtest.Start(t)
	first, second := addTask(t, h), addTask(t, h)
	req := caps()
	req.RequestID = "claim-1"
	host := h.Host("host-a")
	a, err := host.Claim(bg, req)
	if err != nil || a.ID != first.ID {
		t.Fatalf("%+v %v", a, err)
	}
	if b, err := host.Claim(bg, req); err != nil || b != a {
		t.Fatalf("retry got %+v want %+v (%v)", b, a, err)
	}
	req.RequestID = "claim-2"
	if c, err := host.Claim(bg, req); err != nil || c.ID != second.ID {
		t.Fatalf("new id: %+v %v", c, err)
	}

	// Session claims accept the same optional body; an empty body still works.
	sess := h.Session("host-b")
	regSession(t, sess, "w1")
	task, err := sess.AddTask(bg, api.TaskSpec{Repo: project, Prompt: "p", MaxBudgetUSD: 1, TargetSession: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	sa, err := sess.SessionClaimWith(bg, "w1", api.SessionClaimRequest{RequestID: "s-1"})
	if err != nil || sa.ID != task.ID {
		t.Fatalf("%+v %v", sa, err)
	}
	if sb, err := sess.SessionClaimWith(bg, "w1", api.SessionClaimRequest{RequestID: "s-1"}); err != nil || sb != sa {
		t.Fatalf("session retry got %+v want %+v (%v)", sb, sa, err)
	}
	if _, err := sess.SessionClaim(bg, "w1"); !errors.Is(err, client.ErrNoTask) {
		t.Fatalf("empty-body claim: %v", err)
	}
}

func TestHeartbeatOnReapedTaskSaysLeaseReaped(t *testing.T) {
	h := hubtest.Start(t)
	if _, err := h.Admin().AddTask(bg, api.TaskSpec{Repo: "r", Prompt: "p", MaxBudgetUSD: 2, MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	host := h.Host("host-a")
	c, err := host.Claim(bg, caps())
	if err != nil {
		t.Fatal(err)
	}
	h.Clock.Advance(5*time.Minute + 31*time.Minute) // past TTL and the revive window
	if _, err := h.Store.Reap(bg); err != nil {
		t.Fatal(err)
	}
	err = host.Heartbeat(bg, c.ID, api.HeartbeatRequest{Fence: c.Fence})
	if !client.IsLeaseReaped(err) || !client.IsLeaseLost(err) {
		t.Fatalf("heartbeat on reaped task: %v", err)
	}
	if _, err := host.Complete(bg, c.ID, api.CompleteRequest{Fence: c.Fence}); err != nil {
		t.Fatalf("late complete: %v", err)
	}
	err = host.Heartbeat(bg, c.ID, api.HeartbeatRequest{Fence: c.Fence})
	if client.IsLeaseReaped(err) || !client.IsLeaseLost(err) {
		t.Fatalf("heartbeat after late complete: %v", err)
	}
}
