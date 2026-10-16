package mcptools_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/hubtest"
	"github.com/felixn678/agent-mesh/internal/mcptools"
)

var bg = context.Background()

// startHub starts a test hub and waits until it serves, so a short test
// cannot stop it during startup grace (hubtest reports that as an error).
func startHub(t *testing.T) *hubtest.Hub {
	t.Helper()
	h := hubtest.Start(t)
	resp, err := http.Get(h.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return h
}

// connect runs newServer for p over in-memory transports.
func connect(t *testing.T, h *hubtest.Hub, p api.Principal) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := mcptools.NewServerForTest(h.Store, p).Connect(bg, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(bg, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// call invokes a tool and returns its text and IsError. Protocol errors
// (e.g. schema violations) count as errors too.
func call(t *testing.T, cs *mcp.ClientSession, name string, args any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(bg, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error(), true
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func mustOK(t *testing.T, cs *mcp.ClientSession, name string, args any, want string) string {
	t.Helper()
	out, isErr := call(t, cs, name, args)
	if isErr || !strings.Contains(out, want) {
		t.Fatalf("%s(%v) = %q (err=%v), want %q", name, args, out, isErr, want)
	}
	return out
}

func mustErr(t *testing.T, cs *mcp.ClientSession, name string, args any, want string) {
	t.Helper()
	out, isErr := call(t, cs, name, args)
	if !isErr || !strings.Contains(out, want) {
		t.Fatalf("%s(%v) = %q (err=%v), want error %q", name, args, out, isErr, want)
	}
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(bg, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestTaskToolsAdminLifecycle(t *testing.T) {
	h := startHub(t)
	cs := connect(t, h, api.Principal{Admin: true})
	add := map[string]any{"repo": "agent-mesh", "prompt": "fix the flaky test", "budget_usd": 2}
	mustOK(t, cs, "task_add", add, `queued #1 queued repo=agent-mesh f0 a0/`)
	mustOK(t, cs, "task_list", map[string]any{}, `#1 queued repo=agent-mesh`)
	mustOK(t, cs, "task_list", map[string]any{"state": "done"}, "no tasks")
	mustOK(t, cs, "task_get", map[string]any{"id": 1}, "created_by: admin")
	mustOK(t, cs, "task_cancel", map[string]any{"id": 1}, "cancelled #1 cancelled")
	mustOK(t, cs, "task_requeue", map[string]any{"id": 1}, "requeued #1 queued")
	mustErr(t, cs, "task_get", map[string]any{"id": 99}, "task not found")
}

func TestTaskAddRejectsUnsafeSpecs(t *testing.T) {
	h := startHub(t)
	cs := connect(t, h, api.Principal{Host: "host-a", Session: true})
	base := func(k string, v any) map[string]any {
		m := map[string]any{"repo": "r", "prompt": "p", "budget_usd": 1}
		m[k] = v
		return m
	}
	mustErr(t, cs, "task_add", base("permission_mode", "bypassPermissions"), "invalid")
	mustErr(t, cs, "task_add", base("base_branch", "agentmesh/task-1-f1"), "invalid")
	mustErr(t, cs, "task_add", base("allowed_tools", []string{"Bash"}), "allowed_tools")
	out := mustOK(t, cs, "task_add", base("tool_profile", "default"), "queued #1")
	task, _ := h.Store.Get(bg, 1)
	if task.CreatedBy != "host:host-a" {
		t.Fatalf("created_by %q (%s)", task.CreatedBy, out)
	}
}

func TestSessionPrincipalCannotCancel(t *testing.T) {
	h := startHub(t)
	cs := connect(t, h, api.Principal{Host: "host-a", Session: true})
	names := strings.Join(toolNames(t, cs), ",")
	if strings.Contains(names, "task_cancel") || strings.Contains(names, "task_requeue") || !strings.Contains(names, "inbox") {
		t.Fatalf("session tools: %s", names)
	}
	mustErr(t, cs, "task_cancel", map[string]any{"id": 1}, "")
	if !mcptools.AdminOpRefusesNonAdmin(h.Store) {
		t.Fatal("adminOp must refuse non-admin")
	}
	admin := strings.Join(toolNames(t, connect(t, h, api.Principal{Admin: true})), ",")
	if !strings.Contains(admin, "task_requeue") || strings.Contains(admin, "inbox") {
		t.Fatalf("admin tools: %s", admin)
	}
}

func TestToolsListIsSmall(t *testing.T) {
	h := startHub(t)
	res, err := connect(t, h, api.Principal{Admin: true}).ListTools(bg, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if len(b) > 2560 {
		t.Fatalf("tools/list is %d bytes", len(b))
	}
	t.Logf("tools/list: %d bytes, %d tools", len(b), len(res.Tools))
}

func TestHostsAndReportTools(t *testing.T) {
	h := startHub(t)
	cs := connect(t, h, api.Principal{Admin: true})
	mustOK(t, cs, "hosts", map[string]any{}, "no hosts")
	if err := h.Store.UpsertHostLoad(bg, "host-a", api.HostLoad{Cores: 8, RAMGB: 16, Slots: 2, MemAvailPct: 50, CPUModel: "cpu-x"}); err != nil {
		t.Fatal(err)
	}
	mustOK(t, cs, "hosts", map[string]any{}, "host-a: 8c/16GB → 2 slots, running 0, cpu 0% mem 50%, seen ")
	mustOK(t, cs, "hosts", map[string]any{"verbose": true}, "cpu_model cpu-x")
	mustOK(t, cs, "morning_report", map[string]any{}, "eport")
}
