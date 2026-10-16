package hub_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/hubtest"
)

func httpStatus(err error) int {
	var he *client.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

func TestSessionsAndMessagesREST(t *testing.T) {
	h := hubtest.Start(t)
	a, b := h.Session("host-a"), h.Session("host-b")
	s, err := a.RegisterSession(bg, api.RegisterSessionRequest{ID: "sa", Cwd: "/w", Name: "alpha"})
	if err != nil || s.Host != "host-a" || !s.Alive {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := b.RegisterSession(bg, api.RegisterSessionRequest{ID: "sb"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RegisterSession(bg, api.RegisterSessionRequest{ID: "sa"}); httpStatus(err) != 403 {
		t.Fatalf("hijack: %v", err)
	}
	alive, err := h.Admin().ListSessions(bg, true)
	if err != nil || len(alive) != 2 {
		t.Fatalf("%+v %v", alive, err)
	}

	m, err := a.SendMessage(bg, api.SendMessageRequest{FromSession: "sa", ToSession: "sb", Body: "hello"})
	if err != nil || m.FromHost != "host-a" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := a.SendMessage(bg, api.SendMessageRequest{FromSession: "sa", ToSession: "ghost", Body: "x"}); httpStatus(err) != 404 {
		t.Fatalf("unknown target: %v", err)
	}
	if _, err := a.SendMessage(bg, api.SendMessageRequest{FromSession: "sa", ToSession: "sb", Body: strings.Repeat("x", 8193)}); httpStatus(err) != 400 {
		t.Fatalf("oversize: %v", err)
	}
	if _, err := b.SendMessage(bg, api.SendMessageRequest{FromSession: "sa", ToSession: "sb", Body: "spoof"}); httpStatus(err) != 403 {
		t.Fatalf("spoofed sender: %v", err)
	}
	if _, err := a.Inbox(bg, "sb", true); httpStatus(err) != 403 {
		t.Fatalf("foreign inbox: %v", err)
	}
	msgs, err := b.Inbox(bg, "sb", true)
	if err != nil || len(msgs) != 1 || msgs[0].Body != "hello" {
		t.Fatalf("%+v %v", msgs, err)
	}
	if msgs, _ = b.Inbox(bg, "sb", true); len(msgs) != 0 {
		t.Fatalf("redelivered %+v", msgs)
	}
	if err := a.EndSession(bg, "sa"); err != nil {
		t.Fatal(err)
	}
	if alive, _ = h.Admin().ListSessions(bg, true); len(alive) != 1 {
		t.Fatalf("%+v", alive)
	}
}

func TestInboxLongPoll(t *testing.T) {
	h := hubtest.Start(t)
	a, b := h.Session("host-a"), h.Session("host-b")
	a.RegisterSession(bg, api.RegisterSessionRequest{ID: "sa"})
	b.RegisterSession(bg, api.RegisterSessionRequest{ID: "sb"})

	type res struct {
		msgs []api.Message
		err  error
		at   time.Time
	}
	ch := make(chan res, 1)
	go func() {
		m, err := b.InboxWait(bg, "sb", true, 20*time.Second)
		ch <- res{m, err, time.Now()}
	}()
	time.Sleep(200 * time.Millisecond)
	sent := time.Now()
	if _, err := a.SendMessage(bg, api.SendMessageRequest{FromSession: "sa", ToSession: "sb", Body: "ping"}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-ch:
		if r.err != nil || len(r.msgs) != 1 || r.at.Sub(sent) > time.Second {
			t.Fatalf("%+v (latency %v)", r, r.at.Sub(sent))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long poll not woken")
	}

	start := time.Now()
	msgs, err := b.InboxWait(bg, "sb", true, time.Second)
	if err != nil || len(msgs) != 0 || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("timeout case: %v %v after %v", msgs, err, time.Since(start))
	}
	if _, err := b.InboxWait(bg, "sb", true, 0); err != nil {
		t.Fatal(err)
	}
}

func TestInboxWaitRejectsOutOfRange(t *testing.T) {
	h := hubtest.Start(t)
	h.Session("host-a").RegisterSession(bg, api.RegisterSessionRequest{ID: "sa"})
	for _, w := range []string{"51", "51s", "-1", "abc"} {
		if got, _ := do(t, "GET", h.URL+"/api/v1/messages/inbox?session=sa&wait="+w, h.SessionToken("host-a"), ""); got != 400 {
			t.Errorf("wait=%s: %d", w, got)
		}
	}
}

func TestLongPollEndsOnHubStop(t *testing.T) {
	h := hubtest.Start(t)
	b := h.Session("host-b")
	b.RegisterSession(bg, api.RegisterSessionRequest{ID: "sb"})
	done := make(chan struct{})
	go func() { _, _ = b.InboxWait(bg, "sb", true, 30*time.Second); close(done) }()
	time.Sleep(200 * time.Millisecond)
	stopped := make(chan struct{})
	go func() { h.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown blocked by long poll")
	}
}

func TestSessionErrorCodesOverREST(t *testing.T) {
	h := hubtest.Start(t)
	a, b := h.Session("host-a"), h.Session("host-b")
	a.RegisterSession(bg, api.RegisterSessionRequest{ID: "sa"})
	b.RegisterSession(bg, api.RegisterSessionRequest{ID: "sb"})
	code := func(err error) string {
		var he *client.HTTPError
		if errors.As(err, &he) {
			return he.Code
		}
		return ""
	}
	send := func() error {
		_, err := a.SendMessage(bg, api.SendMessageRequest{FromSession: "sa", ToSession: "sb", Body: "m"})
		return err
	}
	for i := 0; i < api.MaxUnreadPerSession; i++ {
		if err := send(); err != nil {
			t.Fatal(err)
		}
	}
	if err := send(); httpStatus(err) != 409 || code(err) != api.CodeInboxFull {
		t.Fatalf("inbox_full: %v", err)
	}
	if err := b.EndSession(bg, "sb"); err != nil {
		t.Fatal(err)
	}
	if err := send(); httpStatus(err) != 409 || code(err) != api.CodeEnded {
		t.Fatalf("ended: %v", err)
	}
	if _, err := b.Inbox(bg, "sb", false); code(err) != api.CodeEnded {
		t.Fatalf("ended inbox: %v", err)
	}
	if err := a.EndSession(bg, "sb"); httpStatus(err) != 403 {
		t.Fatalf("foreign end: %v", err)
	}
}

func TestLongPollsDoNotBlockClaims(t *testing.T) {
	h := hubtest.Start(t)
	s := h.Session("host-a")
	s.RegisterSession(bg, api.RegisterSessionRequest{ID: "sa"})
	ctx, cancel := context.WithCancel(bg)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.InboxWait(ctx, "sa", true, 30*time.Second) }()
	}
	time.Sleep(300 * time.Millisecond)
	addTask(t, h)
	start := time.Now()
	if _, err := h.Host("host-a").Claim(bg, caps()); err != nil || time.Since(start) > time.Second {
		t.Fatalf("claim blocked by waiters: %v after %v", err, time.Since(start))
	}
	cancel()
	wg.Wait()
}
