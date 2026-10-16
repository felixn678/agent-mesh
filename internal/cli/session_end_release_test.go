package cli

import (
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

// Ending a session hands its held tasks back at once (attempt not spent), so
// another session can take them without waiting for the lease to lapse.
func TestSessionEndReleasesHeldTasks(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	hook(t, "session-start", "hook_session_start.json")
	c := h.Session("host-a")
	if _, err := c.RegisterSession(bg, api.RegisterSessionRequest{ID: fixtureSession, Repo: "github.com/o/r"}); err != nil {
		t.Fatal(err)
	}
	task, err := c.AddTask(bg, api.TaskSpec{Repo: "github.com/o/r", Prompt: "p", MaxBudgetUSD: 1, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if ct, err := c.SessionClaim(bg, fixtureSession); err != nil || ct.ID != task.ID {
		t.Fatalf("claim: %+v %v", ct, err)
	}
	if _, out := hook(t, "session-end", "hook_session_end.json"); out != "" {
		t.Fatalf("session-end printed %q", out)
	}
	got, _ := h.Admin().GetTask(bg, task.ID)
	if got.State != api.StateQueued || got.Attempts != 0 || got.Reason != "released by "+store.SessionHolderPrefix+fixtureSession+": session ended" {
		t.Fatalf("held task not released: %+v", got)
	}
	if list, _ := h.Store.ListSessions(bg, true); len(list) != 0 {
		t.Fatalf("session still alive: %+v", list)
	}
	// Another session (max_attempts 1: only possible because no attempt was spent) takes it.
	if _, err := c.RegisterSession(bg, api.RegisterSessionRequest{ID: "other-0001", Repo: "github.com/o/r"}); err != nil {
		t.Fatal(err)
	}
	if ct, err := c.SessionClaim(bg, "other-0001"); err != nil || ct.ID != task.ID {
		t.Fatalf("reclaim: %+v %v", ct, err)
	}
}
