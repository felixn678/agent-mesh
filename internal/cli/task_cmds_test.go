package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// TestTaskAddRetriesWithSameRequestID: the first response is lost after the
// hub stored the task; the retry must get that task back, not a duplicate.
func TestTaskAddRetriesWithSameRequestID(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	var calls atomic.Int32
	var ids []string
	var mu sync.Mutex
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var spec api.TaskSpec
		_ = json.Unmarshal(body, &spec)
		mu.Lock()
		ids = append(ids, spec.RequestID)
		mu.Unlock()
		req, _ := http.NewRequest(r.Method, h.URL+r.URL.Path, bytes.NewReader(body))
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		if calls.Add(1) == 1 { // stored, but the answer never arrives
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxy.Close()
	writeClientTOML(t, proxy.URL)
	c, err := cliClient()
	if err != nil {
		t.Fatal(err)
	}
	task, err := addWithRetry(bg, c, api.TaskSpec{Repo: "r", Prompt: "once", MaxBudgetUSD: 1}, time.Millisecond)
	if err != nil || task.ID != 1 {
		t.Fatalf("%+v %v", task, err)
	}
	if list, _ := h.Store.List(bg, "", time.Time{}); len(list) != 1 {
		t.Fatalf("duplicate tasks: %d", len(list))
	}
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("request ids %q", ids)
	}
}

func TestHostsListsSessionsWithWorkerFlag(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	off := false
	h.Session("host-a").RegisterSession(bg, api.RegisterSessionRequest{ID: "nw-000001", Cwd: "/w/x", Worker: &off})
	out, _, err := runCLI(t, "", "hosts", "--sessions")
	if err != nil || !strings.Contains(out, "sessions (1 live):\n  nw-00000 host-a x worker:off seen ") {
		t.Fatalf("%q %v", out, err)
	}
}
