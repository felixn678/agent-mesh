package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	// cooldownPoll is the shorter messages-only poll used while announced
	// claimable work cools down, so newly added work is noticed quickly.
	cooldownPoll time.Duration
}

var defaultWaitOpts = waitOpts{maxDur: 8 * time.Hour, poll: 50 * time.Second,
	backoffMin: 2 * time.Second, backoffMax: 30 * time.Second, cooldownPoll: 10 * time.Second}

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
		msgs, err := c.Inbox(ctx, *session, true)
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			fmt.Fprintln(std.out, "no unread messages")
			return nil
		}
		fmt.Fprint(std.out, format.Messages(msgs, sendersOf(ctx, c)))
		return nil
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
		resp, err := c.InboxWaitClaimable(ctx, id, true, poll)
		claimWake := err == nil && claimWakeDue(dir, id, resp.ClaimableCount)
		if err == nil && len(resp.Messages) == 0 && resp.ClaimableCount > 0 && !claimWake {
			var msgs []api.Message
			msgs, err = c.InboxWait(ctx, id, true, max(min(o.cooldownPoll, poll), time.Second)) // announced work cooling down: messages only
			resp = api.InboxResponse{Messages: msgs}
		}
		if ctx.Err() != nil {
			return nil // stopped (session exit or signal): nothing to deliver
		}
		var he *client.HTTPError
		switch {
		case err == nil && (len(resp.Messages) > 0 || claimWake):
			fmt.Fprint(std.out, format.Messages(resp.Messages, sendersOf(ctx, c)))
			if claimWake {
				markClaimWake(dir, id, resp.ClaimableCount)
				fmt.Fprint(std.out, claimInstructions(id, resp.ClaimableCount))
			}
			fmt.Fprintf(std.out, "\n[agentmesh] after handling, restart the waiter: `%s` with run_in_background.\n", waiterCommand(id))
			return nil
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
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, o.backoffMax)
	}
}
