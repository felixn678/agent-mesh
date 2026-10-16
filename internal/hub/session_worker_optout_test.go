package hub_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/hubtest"
)

func TestSessionClaimRefusedForNonWorker(t *testing.T) {
	h := hubtest.Start(t)
	c := h.Session("host-a")
	off := false
	if _, err := c.RegisterSession(bg, api.RegisterSessionRequest{ID: "nw", Repo: project, Worker: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddTask(bg, api.TaskSpec{Repo: project, Prompt: "p", MaxBudgetUSD: 1}); err != nil {
		t.Fatal(err)
	}
	_, err := c.SessionClaim(bg, "nw")
	var he *client.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusConflict || he.Code != api.CodeNotWorker {
		t.Fatalf("want 409 not_worker, got %v", err)
	}
}
