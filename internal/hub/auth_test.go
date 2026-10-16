package hub_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/hubtest"
)

func do(t *testing.T, method, url, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestAuthBasics(t *testing.T) {
	h := hubtest.Start(t)
	cases := []struct {
		name, method, path, token string
		want                      int
	}{
		{"healthz open", "GET", "/healthz", "", 200},
		{"no token", "GET", "/api/v1/tasks", "", 401},
		{"wrong token", "GET", "/api/v1/tasks", "nope", 401},
		{"no token host route", "POST", "/api/v1/claim", "", 401},
		{"no token mcp", "POST", "/mcp", "", 401},
		{"admin ok", "GET", "/api/v1/tasks", h.AdminToken, 200},
		{"host reads", "GET", "/api/v1/tasks", h.HostToken("host-a"), 200},
		{"session reads", "GET", "/api/v1/tasks", h.SessionToken("host-a"), 200},
	}
	for _, c := range cases {
		body := ""
		if c.method == "POST" {
			body = "{}"
		}
		if got, _ := do(t, c.method, h.URL+c.path, c.token, body); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

// TestPermissionMatrix pins which token kind may call which endpoint. An
// endpoint is "allowed" when it gets past authentication (any status other
// than 401/403); the body is empty so allowed calls fail later with 400/404.
func TestPermissionMatrix(t *testing.T) {
	h := hubtest.Start(t)
	tokens := []struct{ name, tok string }{
		{"admin", h.AdminToken}, {"host", h.HostToken("host-a")}, {"session", h.SessionToken("host-a")},
	}
	const (
		A = 1 << iota // admin
		H             // host (daemon) token
		S             // session token
	)
	endpoints := []struct {
		method, path string
		allow        int
	}{
		{"POST", "/api/v1/tasks", A | S},
		{"GET", "/api/v1/tasks", A | H | S},
		{"GET", "/api/v1/tasks/1", A | H | S},
		{"GET", "/api/v1/hosts", A | H | S},
		{"GET", "/api/v1/report", A | H | S},
		{"POST", "/api/v1/tasks/1/cancel", A},
		{"POST", "/api/v1/tasks/1/requeue", A},
		{"POST", "/api/v1/claim", H},
		{"POST", "/api/v1/hosts/load", H},
		{"POST", "/api/v1/tasks/1/heartbeat", H},
		{"POST", "/api/v1/tasks/1/complete", H},
		{"POST", "/api/v1/tasks/1/fail", H},
		{"POST", "/api/v1/tasks/1/release", H},
		{"POST", "/api/v1/sessions", S},
		{"POST", "/api/v1/sessions/x/end", S},
		{"POST", "/api/v1/sessions/x/claim", S},
		{"POST", "/api/v1/sessions/x/tasks/1/heartbeat", S},
		{"POST", "/api/v1/sessions/x/tasks/1/complete", S},
		{"POST", "/api/v1/sessions/x/tasks/1/fail", S},
		{"POST", "/api/v1/sessions/x/tasks/1/release", S},
		{"GET", "/api/v1/messages/inbox?session=x&claimable=1", S},
		{"GET", "/api/v1/messages/inbox?session=x", S},
		{"GET", "/api/v1/sessions", A | S},
		{"POST", "/api/v1/messages", A | S},
		{"POST", "/api/v1/messages/ack", S},
		{"POST", "/mcp", A | S},
	}
	for _, e := range endpoints {
		for i, tk := range tokens {
			body := ""
			if e.method == "POST" {
				body = "{}"
			}
			status, _ := do(t, e.method, h.URL+e.path, tk.tok, body)
			allowed := status != 401 && status != 403
			if want := e.allow&(1<<i) != 0; allowed != want {
				t.Errorf("%s %s as %s: status %d, allowed=%v want %v", e.method, e.path, tk.name, status, allowed, want)
			}
		}
	}
}

func TestTaskCreatedByFollowsToken(t *testing.T) {
	h := hubtest.Start(t)
	spec := api.TaskSpec{Repo: "r", Prompt: "p", MaxBudgetUSD: 1}
	fromSession, err := h.Session("host-a").AddTask(bg, spec)
	if err != nil || fromSession.CreatedBy != "host:host-a" {
		t.Fatalf("%+v %v", fromSession, err)
	}
	fromAdmin, _ := h.Admin().AddTask(bg, spec)
	if fromAdmin.CreatedBy != "admin" {
		t.Fatalf("%+v", fromAdmin)
	}
	if _, err := h.Session("host-a").CancelTask(bg, fromAdmin.ID); httpStatus(err) != 403 {
		t.Fatalf("session cancel: %v", err)
	}
	if _, err := h.Host("host-a").AddTask(bg, spec); httpStatus(err) != 403 {
		t.Fatalf("daemon token add: %v", err)
	}
}

func TestMCPMountedBehindAdmin(t *testing.T) {
	h := hubtest.Start(t)
	status, _ := do(t, "POST", h.URL+"/mcp", h.AdminToken, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if status == 401 || status == 403 || status == 404 {
		t.Fatalf("admin cannot reach MCP handler: %d", status)
	}
}

func TestBodyLimit413(t *testing.T) {
	h := hubtest.Start(t)
	big := `{"repo":"r","prompt":"` + strings.Repeat("a", 1<<20) + `"}`
	if got, body := do(t, "POST", h.URL+"/api/v1/tasks", h.AdminToken, big); got != 413 || !strings.Contains(body, "too_large") {
		t.Fatalf("admin: %d %s", got, body)
	}
	if got, _ := do(t, "POST", h.URL+"/api/v1/claim", h.HostToken("host-a"), big); got != 413 {
		t.Fatalf("host: %d", got)
	}
	// Just under the limit is not a 413.
	ok := `{"repo":"r","max_budget_usd":1,"prompt":"` + strings.Repeat("a", 60*1024) + `"}`
	if got, _ := do(t, "POST", h.URL+"/api/v1/tasks", h.AdminToken, ok); got != 201 {
		t.Fatalf("60KB prompt: %d", got)
	}
}

func TestValidationReturns400(t *testing.T) {
	h := hubtest.Start(t)
	bodies := map[string]string{
		"bypass":     `{"repo":"r","prompt":"p","max_budget_usd":1,"permission_mode":"bypassPermissions"}`,
		"auto":       `{"repo":"r","prompt":"p","max_budget_usd":1,"permission_mode":"auto"}`,
		"dash base":  `{"repo":"r","prompt":"p","max_budget_usd":1,"base_branch":"-x"}`,
		"agent base": `{"repo":"r","prompt":"p","max_budget_usd":1,"base_branch":"agentmesh/task-1-f1"}`,
		"bad json":   `{`,
	}
	for name, b := range bodies {
		if got, _ := do(t, "POST", h.URL+"/api/v1/tasks", h.AdminToken, b); got != 400 {
			t.Errorf("%s: got %d", name, got)
		}
	}
	if got, _ := do(t, "GET", h.URL+"/api/v1/tasks?state=bogus", h.AdminToken, ""); got != 400 {
		t.Errorf("bad state: %d", got)
	}
	if got, _ := do(t, "GET", h.URL+"/api/v1/tasks/abc", h.AdminToken, ""); got != 400 {
		t.Errorf("bad id: %d", got)
	}
	if got, _ := do(t, "GET", h.URL+"/api/v1/tasks/999", h.AdminToken, ""); got != 404 {
		t.Errorf("missing: %d", got)
	}
}
