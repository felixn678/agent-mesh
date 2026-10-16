package cli

import (
	"context"
	"github.com/felixn678/agent-mesh/internal/cli/hubenv"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/hubtest"
)

var fastWait = waitOpts{maxDur: time.Hour, poll: 50 * time.Second, backoffMin: 20 * time.Millisecond, backoffMax: 100 * time.Millisecond, cooldownPoll: time.Second}

type waitResult struct {
	out string
	err error
	at  time.Time
}

// startWaiter runs `inbox --wait` in the background for the fixture session.
func startWaiter(t *testing.T, ctx context.Context, o waitOpts) (<-chan waitResult, *syncBuf) {
	t.Helper()
	var out, errOut syncBuf
	done := make(chan waitResult, 1)
	go func() {
		err := runInbox(ctx, cmdIO{in: strings.NewReader(""), out: &out, err: &errOut},
			[]string{"--wait", "--session", fixtureSession}, o)
		done <- waitResult{out: out.String(), err: err, at: time.Now()}
	}()
	return done, &out
}

func registerFixture(t *testing.T, h *hubtest.Hub) {
	t.Helper()
	if _, err := h.Session("host-a").RegisterSession(bg, api.RegisterSessionRequest{ID: fixtureSession, Cwd: "/w/lumio"}); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
	}
}

func TestWaiterWakesOnMessageWithinASecond(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	registerFixture(t, h)
	done, _ := startWaiter(t, bg, fastWait)
	waitFor(t, func() bool { return waiterAlive(waitersDir(), fixtureSession) })
	time.Sleep(100 * time.Millisecond) // let the long poll start
	sent := time.Now()
	peer(t, h, "wake up")
	select {
	case r := <-done:
		if r.err != nil || !strings.Contains(r.out, "1 unread message(s) from host-b/peer-000") ||
			strings.Contains(r.out, "wake up") || !strings.Contains(r.out, "restart the waiter") {
			t.Fatalf("%q %v", r.out, r.err)
		}
		if d := r.at.Sub(sent); d > time.Second {
			t.Fatalf("woke after %v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not wake")
	}
	if waiterAlive(waitersDir(), fixtureSession) {
		t.Fatal("pid file left behind")
	}
	// The waiter only announces: the message stays unread until the prompt
	// hook of the wake-up turn shows and acks it.
	if unreadCount(t, h) != 1 {
		t.Fatal("waiter must not ack")
	}
	// Restarted before the hook ran: the same message does not wake it again.
	again, _ := startWaiter(t, bg, waitOpts{maxDur: 2 * time.Second, poll: time.Second, backoffMin: time.Millisecond, backoffMax: time.Millisecond, cooldownPoll: 100 * time.Millisecond})
	if r := <-again; r.err != nil || !strings.Contains(r.out, "no messages in 2s") {
		t.Fatalf("re-woke for an announced message: %q %v", r.out, r.err)
	}
	if ho, _ := hook(t, "prompt", "hook_prompt.json"); !strings.Contains(ho.HookSpecificOutput.AdditionalContext, "> wake up") || unreadCount(t, h) != 0 {
		t.Fatalf("prompt hook must deliver and ack: %+v", ho)
	}
	// A newer message wakes it again.
	done, _ = startWaiter(t, bg, fastWait)
	waitFor(t, func() bool { return waiterAlive(waitersDir(), fixtureSession) })
	peer(t, h, "second")
	if r := <-done; !strings.Contains(r.out, "1 unread message(s)") {
		t.Fatalf("newer message: %q %v", r.out, r.err)
	}
}

func TestWaiterSurvivesHubRestart(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	registerFixture(t, h)
	done, _ := startWaiter(t, bg, fastWait)
	waitFor(t, func() bool { return waiterAlive(waitersDir(), fixtureSession) })
	h.Stop()
	select {
	case r := <-done:
		t.Fatalf("waiter exited while hub down: %q %v", r.out, r.err)
	case <-time.After(400 * time.Millisecond):
	}
	h.Restart()
	peer(t, h, "back online")
	select {
	case r := <-done:
		if r.err != nil || !strings.Contains(r.out, "unread message(s) from host-b") {
			t.Fatalf("%q %v", r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery after restart")
	}
}

func TestWaiterSingletonAndStalePID(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	registerFixture(t, h)
	dir := waitersDir()
	_ = os.MkdirAll(dir, 0o700)
	// Stale pid file (no such process) and a stale reminder marker.
	_ = os.WriteFile(pidPath(dir, fixtureSession), []byte("2147483646\n"), 0o600)
	_ = os.WriteFile(remindedPath(dir, fixtureSession), nil, 0o600)
	ctx, cancel := context.WithCancel(bg)
	done, _ := startWaiter(t, ctx, fastWait)
	waitFor(t, func() bool { return readPID(pidPath(dir, fixtureSession)) == os.Getpid() })
	if _, err := os.Stat(remindedPath(dir, fixtureSession)); !os.IsNotExist(err) {
		t.Fatal("waiter start must clear the reminder marker")
	}
	out, _, err := runCLI(t, "", "inbox", "--wait", "--session", fixtureSession)
	if err != nil || !strings.Contains(out, "already running (pid ") || !strings.Contains(out, "do not restart") {
		t.Fatalf("second waiter: %q %v", out, err)
	}
	cancel()
	if r := <-done; r.err != nil || !strings.Contains(r.out, "waiter stopped by a signal") {
		t.Fatalf("cancelled waiter: %q %v", r.out, r.err)
	}
}

func TestWaiterExitsOnEndedSessionAndMaxDuration(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	registerFixture(t, h)
	r := <-func() <-chan waitResult {
		d, _ := startWaiter(t, bg, waitOpts{maxDur: time.Second, poll: time.Second, backoffMin: time.Millisecond, backoffMax: time.Millisecond})
		return d
	}()
	if r.err != nil || !strings.Contains(r.out, "no messages in 1s") {
		t.Fatalf("max duration: %q %v", r.out, r.err)
	}
	if err := h.Session("host-a").EndSession(bg, fixtureSession); err != nil {
		t.Fatal(err)
	}
	done, _ := startWaiter(t, bg, fastWait)
	if r := <-done; r.err != nil || r.out != "" {
		t.Fatalf("ended session: %q %v", r.out, r.err)
	}
}

func TestWaiterAuthErrorExits(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	registerFixture(t, h)
	p, _ := hubenv.File(hubenv.SessionTokenFile)
	_ = os.WriteFile(p, []byte("wrong-token\n"), 0o600)
	done, _ := startWaiter(t, bg, fastWait)
	if r := <-done; r.err == nil || !strings.Contains(r.err.Error(), "rejected the session token") {
		t.Fatalf("auth error: %q %v", r.out, r.err)
	}
}

func TestInboxOneShot(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	registerFixture(t, h)
	if out, _, err := runCLI(t, "", "inbox", "--session", fixtureSession); err != nil || out != "no unread messages\n" {
		t.Fatalf("%q %v", out, err)
	}
	peer(t, h, "hello")
	if out, _, err := runCLI(t, "", "inbox", "--session", fixtureSession); err != nil || !strings.Contains(out, "> hello") {
		t.Fatalf("%q %v", out, err)
	}
}
