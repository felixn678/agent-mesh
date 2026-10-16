package mcptools_test

import (
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
)

func TestSessionWorkerLoopViaMCP(t *testing.T) {
	h := startHub(t)
	const repo = "https://github.com/owner/proj.git"
	for _, s := range []struct{ host, id, name string }{{"host-a", "req-0001", "lead"}, {"host-b", "wrk-0001", "worker"}} {
		if _, err := h.Store.RegisterSession(bg, s.host, api.RegisterSessionRequest{ID: s.id, Name: s.name, Repo: repo}); err != nil {
			t.Fatal(err)
		}
	}
	a := connect(t, h, api.Principal{Host: "host-a", Session: true})
	b := connect(t, h, api.Principal{Host: "host-b", Session: true})

	mustErr(t, a, "task_add", map[string]any{"prompt": "x", "budget_usd": 1}, "repo is required")
	// Repo defaults to the creator session's repo; target resolved by name.
	mustOK(t, a, "task_add", map[string]any{"from": "req-0001", "target_session": "worker", "prompt": "add a README badge", "budget_usd": 1},
		"queued #1 queued repo=github.com/owner/proj")
	task, _ := h.Store.Get(bg, 1)
	if task.TargetSession != "wrk-0001" || task.CreatedBySession != "req-0001" {
		t.Fatalf("targeting: %+v", task)
	}
	// The requester is not the target, so it cannot claim it.
	mustOK(t, a, "task_claim", map[string]any{"session": "req-0001"}, "no claimable task")
	mustErr(t, b, "task_claim", map[string]any{"session": "req-0001"}, "another host")

	out := mustOK(t, b, "task_claim", map[string]any{"session": "wrk-0001"}, "claimed task #1 fence 1 repo=github.com/owner/proj")
	if !strings.Contains(out, "add a README badge") || !strings.Contains(out, "task_done(session, id=1, fence=1") {
		t.Fatalf("claim output:\n%s", out)
	}
	mustOK(t, b, "task_heartbeat", map[string]any{"session": "wrk-0001", "id": 1, "fence": 1}, "lease extended")
	mustErr(t, b, "task_done", map[string]any{"session": "wrk-0001", "id": 1, "fence": 2, "summary": "x"}, "lease lost")
	mustOK(t, b, "task_done", map[string]any{"session": "wrk-0001", "id": 1, "fence": 1, "summary": "badge added", "branch": "feat/badge"},
		"done: #1 done session:wrk-0001")
	mustOK(t, b, "task_done", map[string]any{"session": "wrk-0001", "id": 1, "fence": 1, "summary": "badge added"}, "already recorded")
	// The hub messages the creator session on completion.
	mustOK(t, a, "inbox", map[string]any{"session": "req-0001"}, "badge added")

	// Fail with retry requeues; release gives it back without an attempt.
	mustOK(t, a, "task_add", map[string]any{"from": "req-0001", "target_host": "host-b", "prompt": "second", "budget_usd": 1}, "queued #2")
	mustOK(t, b, "task_claim", map[string]any{"session": "wrk-0001"}, "claimed task #2 fence 1")
	mustOK(t, b, "task_fail", map[string]any{"session": "wrk-0001", "id": 2, "fence": 1, "reason": "tests red", "retry": true}, "failed: #2 queued")
	mustOK(t, b, "task_claim", map[string]any{"session": "wrk-0001"}, "claimed task #2 fence 2")
	mustOK(t, b, "task_release", map[string]any{"session": "wrk-0001", "id": 2, "fence": 2, "reason": "busy"}, "released: #2 queued")
	if names := strings.Join(toolNames(t, connect(t, h, api.Principal{Admin: true})), ","); strings.Contains(names, "task_claim") {
		t.Fatalf("admin must not see worker tools: %s", names)
	}
}
