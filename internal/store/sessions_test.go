package store_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

func reg(t *testing.T, e *env, host, id string) api.Session {
	t.Helper()
	s, err := e.st.RegisterSession(bg, host, api.RegisterSessionRequest{ID: id, Cwd: "/work/x", Name: "n"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSessionRegisterHeartbeatAndAlive(t *testing.T) {
	e := newEnv(t)
	s := reg(t, e, "box", "s1")
	if !s.Alive || s.Host != "box" || s.StartedAt == 0 {
		t.Fatalf("%+v", s)
	}
	e.clock.Advance(13 * time.Hour)
	if list, _ := e.st.ListSessions(bg, true); len(list) != 0 {
		t.Fatalf("stale session listed alive: %+v", list)
	}
	if list, _ := e.st.ListSessions(bg, false); len(list) != 1 || list[0].Alive {
		t.Fatalf("%+v", list)
	}
	again := reg(t, e, "box", "s1") // heartbeat
	if !again.Alive || again.StartedAt != s.StartedAt {
		t.Fatalf("%+v", again)
	}
}

func TestSessionOwnershipAndEnd(t *testing.T) {
	e := newEnv(t)
	reg(t, e, "box", "s1")
	_, err := e.st.RegisterSession(bg, "evil", api.RegisterSessionRequest{ID: "s1"})
	wantErr(t, err, store.ErrSessionOwner)
	wantErr(t, e.st.EndSession(bg, "evil", api.EndSessionRequest{ID: "s1"}), store.ErrSessionOwner)
	wantErr(t, e.st.EndSession(bg, "box", api.EndSessionRequest{ID: "nope"}), store.ErrSessionNotFound)
	if err := e.st.EndSession(bg, "box", api.EndSessionRequest{ID: "s1"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.EndSession(bg, "", api.EndSessionRequest{ID: "s1"}); err != nil { // admin, idempotent
		t.Fatal(err)
	}
	if list, _ := e.st.ListSessions(bg, true); len(list) != 0 {
		t.Fatalf("ended session alive: %+v", list)
	}
	if !api.IsValidation(func() error {
		_, err := e.st.RegisterSession(bg, "box", api.RegisterSessionRequest{ID: "bad id"})
		return err
	}()) {
		t.Fatal("bad id accepted")
	}
}

func TestMessagesSendInboxOrderAndMarkRead(t *testing.T) {
	e := newEnv(t)
	reg(t, e, "a", "sa")
	reg(t, e, "b", "sb")
	for _, body := range []string{"one", "two", `{"json":"opaque <b>"}`} {
		if _, err := e.st.SendMessage(bg, "a", api.SendMessageRequest{FromSession: "sa", ToSession: "sb", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	peek, err := e.st.Inbox(bg, "b", "sb", false)
	if err != nil || len(peek) != 3 || peek[0].Body != "one" || peek[2].Body != `{"json":"opaque <b>"}` || peek[0].FromHost != "a" {
		t.Fatalf("%+v %v", peek, err)
	}
	if again, _ := e.st.Inbox(bg, "b", "sb", false); len(again) != 3 {
		t.Fatal("peek consumed messages")
	}
	got, _ := e.st.Inbox(bg, "b", "sb", true)
	if len(got) != 3 || got[0].Body != "one" || got[1].Body != "two" || got[0].ReadAt == 0 {
		t.Fatalf("%+v", got)
	}
	if left, _ := e.st.Inbox(bg, "b", "sb", true); len(left) != 0 {
		t.Fatalf("read messages redelivered: %+v", left)
	}
}

func TestMessageGuards(t *testing.T) {
	e := newEnv(t)
	reg(t, e, "a", "sa")
	reg(t, e, "b", "sb")
	send := func(owner string, r api.SendMessageRequest) error {
		_, err := e.st.SendMessage(bg, owner, r)
		return err
	}
	wantErr(t, send("a", api.SendMessageRequest{FromSession: "sa", ToSession: "ghost", Body: "x"}), store.ErrSessionNotFound)
	wantErr(t, send("a", api.SendMessageRequest{FromSession: "ghost", ToSession: "sb", Body: "x"}), store.ErrSessionNotFound)
	wantErr(t, send("b", api.SendMessageRequest{FromSession: "sa", ToSession: "sb", Body: "x"}), store.ErrSessionOwner) // spoof sender
	if err := send("a", api.SendMessageRequest{FromSession: "sa", ToSession: "sb", Body: strings.Repeat("x", api.MaxMessageBytes+1)}); !api.IsValidation(err) {
		t.Fatalf("oversize accepted: %v", err)
	}
	if err := send("a", api.SendMessageRequest{FromSession: "sa", ToSession: "sb", Body: strings.Repeat("x", api.MaxMessageBytes)}); err != nil {
		t.Fatalf("max-size body rejected: %v", err)
	}
	if err := send("", api.SendMessageRequest{FromSession: "sa", ToSession: "sb", Body: "admin"}); err != nil {
		t.Fatal(err)
	}
	_, err := e.st.Inbox(bg, "a", "sb", true) // read someone else's inbox
	wantErr(t, err, store.ErrSessionOwner)
	_, err = e.st.Inbox(bg, "b", "ghost", true)
	wantErr(t, err, store.ErrSessionNotFound)
}

func TestInboxCap(t *testing.T) {
	e := newEnv(t)
	reg(t, e, "a", "sa")
	for i := 0; i < api.InboxLimit+5; i++ {
		if _, err := e.st.SendMessage(bg, "a", api.SendMessageRequest{FromSession: "sa", ToSession: "sa", Body: "m"}); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := e.st.Inbox(bg, "a", "sa", true)
	second, _ := e.st.Inbox(bg, "a", "sa", true)
	if len(first) != api.InboxLimit || len(second) != 5 || second[0].ID <= first[len(first)-1].ID {
		t.Fatalf("%d %d", len(first), len(second))
	}
}

func TestReaperPrunesOldSessionsAndMessages(t *testing.T) {
	e := newEnv(t)
	reg(t, e, "a", "old")
	reg(t, e, "a", "ended")
	if err := e.st.EndSession(bg, "a", api.EndSessionRequest{ID: "ended"}); err != nil {
		t.Fatal(err)
	}
	e.st.SendMessage(bg, "a", api.SendMessageRequest{FromSession: "old", ToSession: "old", Body: "unread"})
	e.st.Inbox(bg, "a", "ended", true)
	e.clock.Advance(6 * 24 * time.Hour)
	reg(t, e, "a", "fresh") // keeps being refreshed
	res, err := e.st.Reap(bg)
	if err != nil || res.SessionsPruned != 0 || res.MessagesPruned != 0 {
		t.Fatalf("pruned too early: %+v %v", res, err)
	}
	e.clock.Advance(2 * 24 * time.Hour)
	reg(t, e, "a", "fresh")
	res, err = e.st.Reap(bg)
	if err != nil || res.SessionsPruned != 2 || res.MessagesPruned != 1 {
		t.Fatalf("want 2 sessions + orphan message pruned: %+v %v", res, err)
	}
	if list, _ := e.st.ListSessions(bg, false); len(list) != 1 || list[0].ID != "fresh" {
		t.Fatalf("%+v", list)
	}
}

func TestWaitInboxWakesOnSend(t *testing.T) {
	e := newEnv(t)
	reg(t, e, "a", "sa")
	reg(t, e, "b", "sb")
	type result struct {
		msgs []api.Message
		err  error
		took time.Duration
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		m, err := e.st.WaitInbox(bg, "b", "sb", true, 10*time.Second)
		done <- result{m, err, time.Since(start)}
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err := e.st.SendMessage(bg, "a", api.SendMessageRequest{FromSession: "sa", ToSession: "sb", Body: "wake"}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || len(r.msgs) != 1 || r.msgs[0].Body != "wake" || r.took > time.Second {
			t.Fatalf("%+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter not woken")
	}
}

func TestWaitInboxTimeoutAndImmediateAndCancel(t *testing.T) {
	e := newEnv(t)
	reg(t, e, "a", "sa")
	start := time.Now()
	msgs, err := e.st.WaitInbox(bg, "a", "sa", true, 300*time.Millisecond)
	if err != nil || len(msgs) != 0 || time.Since(start) < 250*time.Millisecond {
		t.Fatalf("timeout: %v %v %v", msgs, err, time.Since(start))
	}
	e.st.SendMessage(bg, "a", api.SendMessageRequest{FromSession: "sa", ToSession: "sa", Body: "x"})
	start = time.Now()
	if msgs, _ = e.st.WaitInbox(bg, "a", "sa", true, 5*time.Second); len(msgs) != 1 || time.Since(start) > time.Second {
		t.Fatalf("pending message must return immediately: %v", msgs)
	}
	ctx, cancel := context.WithTimeout(bg, 100*time.Millisecond)
	defer cancel()
	start = time.Now()
	if msgs, err := e.st.WaitInbox(ctx, "a", "sa", true, 5*time.Second); err != nil || len(msgs) != 0 || time.Since(start) > time.Second {
		t.Fatalf("cancelled wait must end empty without error: %v %v %v", msgs, err, time.Since(start))
	}
	_, err = e.st.WaitInbox(bg, "other", "sa", true, time.Second)
	wantErr(t, err, store.ErrSessionOwner)
}

func TestSendToEndedSessionAndEndedInbox(t *testing.T) {
	e := newEnv(t)
	reg(t, e, "a", "sa")
	reg(t, e, "b", "sb")
	if err := e.st.EndSession(bg, "b", api.EndSessionRequest{ID: "sb"}); err != nil {
		t.Fatal(err)
	}
	_, err := e.st.SendMessage(bg, "a", api.SendMessageRequest{FromSession: "sa", ToSession: "sb", Body: "x"})
	wantErr(t, err, store.ErrSessionEnded)
	_, err = e.st.Inbox(bg, "b", "sb", true)
	wantErr(t, err, store.ErrSessionEnded)
	_, err = e.st.WaitInbox(bg, "b", "sb", true, time.Second)
	wantErr(t, err, store.ErrSessionEnded)
	// Re-registering revives it.
	reg(t, e, "b", "sb")
	if _, err := e.st.SendMessage(bg, "a", api.SendMessageRequest{FromSession: "sa", ToSession: "sb", Body: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestInboxFullAtHundredUnread(t *testing.T) {
	e := newEnv(t)
	reg(t, e, "a", "sa")
	send := func() error {
		_, err := e.st.SendMessage(bg, "a", api.SendMessageRequest{FromSession: "sa", ToSession: "sa", Body: "m"})
		return err
	}
	for i := 0; i < api.MaxUnreadPerSession; i++ {
		if err := send(); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	wantErr(t, send(), store.ErrInboxFull)
	if _, err := e.st.Inbox(bg, "a", "sa", true); err != nil { // reads 50
		t.Fatal(err)
	}
	if err := send(); err != nil {
		t.Fatalf("space freed by reading: %v", err)
	}
}

func TestConcurrentMarkReadDeliversOnce(t *testing.T) {
	e := newEnv(t)
	reg(t, e, "a", "sa")
	for i := 0; i < 40; i++ {
		e.st.SendMessage(bg, "a", api.SendMessageRequest{FromSession: "sa", ToSession: "sa", Body: "m"})
	}
	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msgs, err := e.st.Inbox(bg, "a", "sa", true)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			for _, m := range msgs {
				seen[m.ID]++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != 40 {
		t.Fatalf("delivered %d distinct", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("message %d delivered %d times", id, n)
		}
	}
}

func TestSessionAliveWindowIsTwelveHours(t *testing.T) {
	e := newEnv(t)
	reg(t, e, "a", "idle")
	e.clock.Advance(11 * time.Hour)
	if l, _ := e.st.ListSessions(bg, true); len(l) != 1 {
		t.Fatal("idle session within 12h must stay alive")
	}
	e.clock.Advance(2 * time.Hour)
	if l, _ := e.st.ListSessions(bg, true); len(l) != 0 {
		t.Fatal("session unseen > 12h must not be alive")
	}
}
