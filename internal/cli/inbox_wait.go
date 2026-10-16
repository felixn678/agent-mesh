package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/mcptools/format"
)

// waitOpts tunes the idle-wake waiter (tests shorten it).
type waitOpts struct {
	maxDur     time.Duration // give up (exit 0 with a notice) after this
	poll       time.Duration // long-poll length per request (hub caps at 50 s)
	backoffMin time.Duration // first retry delay after a network/hub error
	backoffMax time.Duration
	// cooldownPoll is the pause between polls while only already-announced
	// messages or cooling-down work are pending (the hub answers at once).
	cooldownPoll time.Duration
	// parentGone reports that the Claude session that started the waiter is
	// gone (nil: watch the real parent chain every parentCheck).
	parentGone  func() bool
	parentCheck time.Duration
}

var defaultWaitOpts = waitOpts{maxDur: 8 * time.Hour, poll: 50 * time.Second,
	backoffMin: 2 * time.Second, backoffMax: 30 * time.Second, cooldownPoll: 10 * time.Second, parentCheck: 2 * time.Second}

// runInbox prints unread messages once, or with --wait runs the background
// waiter: Claude re-invokes the session when a run_in_background command
// exits, so the waiter exits only when it has something to deliver (messages
// or newly claimable tasks).
func runInbox(ctx context.Context, std cmdIO, args []string, o waitOpts) error {
	fs := newFlags("inbox", std)
	session := fs.String("session", "", "your agentmesh session id")
	wait := fs.Bool("wait", false, "block until a message arrives (background waiter)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !api.ValidSessionID(*session) {
		return errors.New("inbox: --session <id> is required")
	}
	if *wait && hooksOff() {
		return nil
	}
	c, err := sessionClient()
	if err != nil {
		return err
	}
	if !*wait {
		msgs, err := c.Inbox(ctx, *session, false)
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			fmt.Fprintln(std.out, "no unread messages")
			return nil
		}
		text, n := format.Messages(msgs, sendersOf(ctx, c))
		return printThenAck(ctx, std, c, *session, text, msgs[:n])
	}
	dir := waitersDir()
	if dir == "" {
		return errors.New("inbox: cannot locate the state directory")
	}
	release, holder, err := acquireWaiter(dir, *session)
	if err != nil {
		return err
	}
	if release == nil {
		fmt.Fprintf(std.out, "agentmesh waiter already running (pid %d); do not restart\n", holder)
		return nil
	}
	defer release()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	ctx, stopHUP := signal.NotifyContext(ctx, syscall.SIGHUP) // terminal closed
	defer stopHUP()
	gone := o.parentGone
	if gone == nil {
		gone = parentWatcher()
	}
	go watchParent(ctx, cancel, gone, o.parentCheck)
	return waitLoop(ctx, std, c, dir, *session, o)
}

// waitLoop long-polls for messages or claimable tasks. Claimable work wakes
// the session once per new batch (see claimWakeDue); while that wake is
// cooling down the poll waits for messages only, so it never spins.
func waitLoop(ctx context.Context, std cmdIO, c *client.Client, dir, id string, o waitOpts) error {
	deadline := time.Now().Add(o.maxDur)
	backoff := o.backoffMin
	for {
		left := time.Until(deadline)
		if left <= 0 {
			fmt.Fprintf(std.out, "agentmesh: no messages in %s; restart the waiter if still wanted\n", o.maxDur)
			return nil
		}
		poll := min(o.poll, left)
		if poll < time.Second {
			poll = time.Second // the hub's wait granularity
		}
		resp, err := c.InboxWaitClaimable(ctx, id, false, poll)
		if ctx.Err() != nil {
			return stopped(ctx, std)
		}
		fresh := err == nil && newSinceAnnounced(dir, id, resp.Messages)
		claimWake := err == nil && workerMode() && claimWakeDue(dir, id, resp.ClaimableCount)
		var he *client.HTTPError
		switch {
		case fresh || claimWake:
			// Announce only: the wake-up turn runs the UserPromptSubmit hook,
			// which injects the messages into context and acks them. Text
			// in this command's output file may never be read by the model.
			text := ""
			if fresh {
				markAnnounced(dir, id, resp.Messages)
				text = announce(id, resp.Messages)
			}
			if claimWake {
				markClaimWake(dir, id, resp.ClaimableCount)
				text += claimInstructions(id, resp.ClaimableCount)
			}
			text += fmt.Sprintf("[agentmesh] after handling, restart the waiter: `%s` with run_in_background.\n", waiterCommand(id))
			_, err := io.WriteString(std.out, text)
			return err
		case err == nil && (len(resp.Messages) > 0 || resp.ClaimableCount > 0):
			// Only already-announced messages or cooling-down work: the hub
			// answers at once, so pause instead of spinning.
			if sleepCtx(ctx, o.cooldownPoll) != nil {
				return stopped(ctx, std)
			}
			continue
		case err == nil:
			backoff = o.backoffMin
			continue
		case errors.As(err, &he) && (he.Status == http.StatusUnauthorized || he.Status == http.StatusForbidden):
			return fmt.Errorf("inbox: hub rejected the session token: %w", err)
		case errors.As(err, &he) && he.Code == api.CodeEnded:
			return nil
		case errors.As(err, &he) && he.Status == http.StatusNotFound:
			return fmt.Errorf("inbox: session %s is not registered with the hub", id)
		}
		// Hub down or overloaded: retry quietly; exiting would wake the
		// session for nothing.
		if sleepCtx(ctx, backoff) != nil {
			return stopped(ctx, std)
		}
		backoff = min(backoff*2, o.backoffMax)
	}
}

// stopped explains a signal exit: a silent exit 0 made a live session guess
// at a bug and restart the waiter outside run_in_background.
func stopped(ctx context.Context, std cmdIO) error {
	if errors.Is(context.Cause(ctx), errParentGone) {
		return nil // nobody is left to read output
	}
	fmt.Fprintln(std.out, "[agentmesh] waiter stopped by a signal (no message); restart it with run_in_background if you still want idle wake-ups.")
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// announce tells the woken session that messages are waiting, without their
// bodies (the prompt hook delivers those).
func announce(id string, msgs []api.Message) string {
	from := make([]string, 0, len(msgs))
	for _, m := range msgs {
		from = append(from, format.Clean(m.FromHost)+"/"+format.ID8(m.FromSession))
	}
	return fmt.Sprintf("[agentmesh] %d unread message(s) from %s. They are added to your context with this wake-up "+
		"(agentmesh prompt hook); if you do not see them, call the agentmesh inbox tool with session=%s.\n",
		len(msgs), strings.Join(from, ", "), id)
}

func announcedPath(dir, id string) string { return filepath.Join(dir, id+".announced") }

// newSinceAnnounced reports whether msgs holds an id above the highest one
// this session was already woken for, so an unacked message (hook failed)
// cannot wake the session in a loop.
func newSinceAnnounced(dir, id string, msgs []api.Message) bool {
	b, _ := os.ReadFile(announcedPath(dir, id))
	mark, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	for _, m := range msgs {
		if m.ID > mark {
			return true
		}
	}
	return false
}

func markAnnounced(dir, id string, msgs []api.Message) {
	var top int64
	for _, m := range msgs {
		top = max(top, m.ID)
	}
	_ = os.WriteFile(announcedPath(dir, id), []byte(strconv.FormatInt(top, 10)+"\n"), 0o600)
}

// printThenAck writes text and only then acks the messages it rendered, so a
// crash or failed write leaves them unread for the next delivery
// (at-least-once). A failed ack is not an error: they show up again.
func printThenAck(ctx context.Context, std cmdIO, c *client.Client, id, text string, shown []api.Message) error {
	if _, err := io.WriteString(std.out, text); err != nil {
		return err
	}
	if len(shown) > 0 {
		_, _ = c.AckMessages(ctx, id, format.IDs(shown))
	}
	return nil
}
