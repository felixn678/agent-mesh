package cli

import (
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
)

func TestTaskCommandsAgainstHub(t *testing.T) {
	h := startHub(t)
	testEnv(t, h) // session.token present, no admin token
	out, _, err := runCLI(t, "prompt from stdin\nsecond line", "task", "add", "--repo", "agent-mesh", "--prompt-file", "-", "--budget", "1.5", "--mode", "plan")
	if err != nil || !strings.HasPrefix(out, `queued #1 queued repo=agent-mesh f0 a0/`) || !strings.Contains(out, `"prompt from stdin"`) {
		t.Fatalf("add: %q %v", out, err)
	}
	task, _ := h.Store.Get(bg, 1)
	if task.Prompt != "prompt from stdin\nsecond line" || task.PermissionMode != "plan" || task.CreatedBy != "host:host-a" {
		t.Fatalf("stored %+v", task)
	}
	if _, _, err := runCLI(t, "", "task", "add", "--repo", "r", "--prompt", "p", "--mode", "bypassPermissions"); err == nil {
		t.Fatal("bypassPermissions must be rejected")
	}
	if _, _, err := runCLI(t, "", "task", "add", "--repo", "r"); err == nil {
		t.Fatal("missing prompt must be rejected")
	}
	runCLI(t, "", "task", "add", "--repo", "other", "--prompt", "two", "--budget", "1")
	out, _, err = runCLI(t, "", "task", "list")
	if err != nil || out != "#2 queued repo=other f0 a0/3 $0.00 \"two\"\n#1 queued repo=agent-mesh f0 a0/3 $0.00 \"prompt from stdin\"\n" {
		t.Fatalf("list: %q %v", out, err)
	}
	if out, _, _ = runCLI(t, "", "task", "get", "1"); !strings.Contains(out, "mode: plan") || !strings.Contains(out, "second line") {
		t.Fatalf("get: %q", out)
	}
	// Session token cannot cancel; admin token via env can.
	if _, _, err := runCLI(t, "", "task", "cancel", "1"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("session cancel: %v", err)
	}
	t.Setenv("AGENTMESH_TOKEN", h.AdminToken)
	if out, _, err = runCLI(t, "", "task", "cancel", "1"); err != nil || !strings.HasPrefix(out, "cancelled #1 cancelled") {
		t.Fatalf("cancel: %q %v", out, err)
	}
	if out, _, err = runCLI(t, "", "task", "requeue", "1"); err != nil || !strings.HasPrefix(out, "requeued #1 queued") {
		t.Fatalf("requeue: %q %v", out, err)
	}
	if out, _, _ = runCLI(t, "", "task", "list", "--state", "done"); out != "no tasks\n" {
		t.Fatalf("filtered list: %q", out)
	}
}

func TestHostsCommand(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	if out, _, err := runCLI(t, "", "hosts"); err != nil || out != "no hosts\n" {
		t.Fatalf("%q %v", out, err)
	}
	if err := h.Host("host-a").ReportLoad(bg, api.HostLoad{Cores: 8, RAMGB: 8, Slots: 1, MemAvailPct: 70, CPUModel: "cpu-x"}); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "hosts", "--verbose")
	if err != nil || !strings.HasPrefix(out, "host-a: 8c/8GB → 1 slot, running 0, cpu 0% mem 30%, seen ") || !strings.Contains(out, "cpu_model cpu-x") {
		t.Fatalf("%q %v", out, err)
	}
}
