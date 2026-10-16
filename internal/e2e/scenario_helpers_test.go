package e2e_test

import (
	"errors"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
)

// Helpers shared by the two-host scenarios.

func addTask(t *testing.T, c *client.Client, prompt, targetHost string) api.Task {
	t.Helper()
	task, err := c.AddTask(bg, api.TaskSpec{Repo: "git@github.com:acme/lumio.git", Prompt: prompt, MaxBudgetUSD: 1,
		TargetHost: targetHost, CreatedBySession: "a-boss"})
	if err != nil || task.Repo != lumio {
		t.Fatalf("add task: %+v %v", task, err)
	}
	return task
}

// noClaimable asserts the other-repo session never sees work.
func noClaimable(t *testing.T, c *client.Client) {
	t.Helper()
	r, err := c.InboxWaitClaimable(bg, "m-other", false, 0)
	if err != nil || r.ClaimableCount != 0 {
		t.Fatalf("other-repo session sees %d claimable (%v)", r.ClaimableCount, err)
	}
	if _, err := c.SessionClaim(bg, "m-other"); !errors.Is(err, client.ErrNoTask) {
		t.Fatalf("other-repo session claimed: %v", err)
	}
}

// drainNotices fetches creator notices until n arrived, then acks them.
func drainNotices(t *testing.T, c *client.Client, n int) []api.Message {
	t.Helper()
	var got []api.Message
	within(t, 5*time.Second, "creator notices", func() bool {
		msgs, err := c.InboxWait(bg, "a-boss", false, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		got = msgs
		return len(msgs) >= n
	})
	ids := make([]int64, 0, len(got))
	for _, m := range got {
		if m.FromSession != "hub" {
			t.Fatalf("unexpected sender %+v", m)
		}
		ids = append(ids, m.ID)
	}
	if acked, err := c.AckMessages(bg, "a-boss", ids); err != nil || acked != int64(len(ids)) {
		t.Fatalf("ack %d: %d %v", len(ids), acked, err)
	}
	return got
}
