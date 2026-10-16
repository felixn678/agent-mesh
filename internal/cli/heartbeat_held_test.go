package cli

import (
	"context"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
)

// One refused heartbeat (a held task whose lease lapsed past the revive
// window) must not keep the session's other held tasks from being extended.
func TestHeartbeatHeldExtendsEveryTaskDespiteOneRefusal(t *testing.T) {
	h := startHub(t)
	ctx := context.Background()
	c := h.Session("host-a")
	if _, err := c.RegisterSession(ctx, api.RegisterSessionRequest{ID: "w1", Repo: "github.com/o/r"}); err != nil {
		t.Fatal(err)
	}
	add := func(repo string, max int) {
		if _, err := c.AddTask(ctx, api.TaskSpec{Repo: repo, Prompt: "p", MaxBudgetUSD: 1, MaxAttempts: max}); err != nil {
			t.Fatal(err)
		}
	}
	// The healthy task gets the lower id, so the newest-first list reaches
	// the refused one first.
	add("github.com/o/other", 3)
	add("github.com/o/r", 1) // at its cap once claimed: never reclaimable, so it stays held
	first, err := c.SessionClaim(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	h.Clock.Advance(30*time.Minute + 31*time.Minute) // lease lapsed past the 30 min revive window
	if _, err := c.RegisterSession(ctx, api.RegisterSessionRequest{ID: "w1", Repo: "github.com/o/other"}); err != nil {
		t.Fatal(err)
	}
	second, err := c.SessionClaim(ctx, "w1")
	if err != nil || second.ID == first.ID || second.ID > first.ID {
		t.Fatalf("second claim: %+v %v", second, err)
	}
	h.Clock.Advance(20 * time.Minute)
	err = heartbeatHeld(ctx, c, "w1")
	if !client.IsLeaseLost(err) {
		t.Fatalf("want the lapsed task reported, got %v", err)
	}
	got, _ := h.Admin().GetTask(ctx, second.ID)
	if got.LeaseExpiresAt != h.Clock.Now().Add(30*time.Minute).UnixMilli() {
		t.Fatalf("second task not extended: lease %d now %d", got.LeaseExpiresAt, h.Clock.Now().UnixMilli())
	}
}
