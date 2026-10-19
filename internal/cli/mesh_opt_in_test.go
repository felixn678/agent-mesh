package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/hubtest"
)

func TestMeshToggle(t *testing.T) {
	for prompt, want := range map[string]string{
		"mesh on": "on", "  Mesh   ON \nthen message mac": "on", "MESH OFF": "off",
		"turn mesh on": "", "mesh only": "", "run the tests": "", "": "",
	} {
		if got := meshToggle(prompt); got != want {
			t.Errorf("meshToggle(%q) = %q, want %q", prompt, got, want)
		}
	}
}

// A session nobody turned on never reaches the hub: no output, no registration.
func TestHooksStaySilentOutsideTheMesh(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	t.Setenv("AGENTMESH", "")
	known(t, h)
	peer(t, h, "hello")
	for _, ev := range []struct{ event, fixture string }{
		{"session-start", "hook_session_start.json"}, {"prompt", "hook_prompt.json"},
		{"stop", "hook_stop.json"}, {"session-end", "hook_session_end.json"},
	} {
		if _, out := hook(t, ev.event, ev.fixture); out != "" {
			t.Fatalf("%s printed %q", ev.event, out)
		}
	}
	list, _ := h.Store.ListSessions(bg, true)
	if len(list) != 2 {
		t.Fatalf("sessions: %+v", list)
	}
	for _, s := range list {
		if s.ID == fixtureSession && s.Cwd != "/w" {
			t.Fatalf("a hook re-registered the session: %+v", s)
		}
	}
	if unreadCount(t, h) != 1 {
		t.Fatal("a session outside the mesh must not consume messages")
	}
}

// known registers the fixture session so peers can message it.
func known(t *testing.T, h *hubtest.Hub) {
	t.Helper()
	if _, err := h.Session("host-a").RegisterSession(bg, api.RegisterSessionRequest{ID: fixtureSession, Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
}

func meshPrompt(t *testing.T, prompt string) hookOut {
	t.Helper()
	in := strings.Replace(fixture(t, "hook_prompt.json"), `"run the tests"`, `"`+prompt+`"`, 1)
	out, _, err := runCLI(t, in, "hook", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	var ho hookOut
	if out != "" {
		if err := json.Unmarshal([]byte(out), &ho); err != nil {
			t.Fatalf("prompt output is not JSON: %q", out)
		}
	}
	return ho
}

// `mesh on` joins and briefs the session and sticks across later hooks;
// `mesh off` ends it on the hub and silences the hooks again.
func TestMeshOnAndOffByPrompt(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	t.Setenv("AGENTMESH", "")
	known(t, h)
	peer(t, h, "hello")

	ctx := meshPrompt(t, "mesh on").HookSpecificOutput.AdditionalContext
	for _, want := range []string{"typed `mesh on`", "your session id is " + fixtureSession, "inbox --wait --session", "hello", "SendMessage"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("mesh on context lacks %q:\n%s", want, ctx)
		}
	}
	peer(t, h, "second")
	if ho, _ := hook(t, "prompt", "hook_prompt.json"); !strings.Contains(ho.HookSpecificOutput.AdditionalContext, "second") {
		t.Fatalf("a session in the mesh should get its messages: %+v", ho)
	}

	if ctx := meshPrompt(t, "mesh off").HookSpecificOutput.AdditionalContext; !strings.Contains(ctx, "left the mesh") {
		t.Fatalf("mesh off context: %q", ctx)
	}
	if list, _ := h.Store.ListSessions(bg, true); len(list) != 1 {
		t.Fatalf("mesh off did not end the session: %+v", list)
	}
	t.Setenv("AGENTMESH", "on") // a recorded `mesh off` wins over the env
	if _, out := hook(t, "prompt", "hook_prompt.json"); out != "" {
		t.Fatalf("hooks after mesh off printed %q", out)
	}
	if ctx := meshPrompt(t, "mesh on").HookSpecificOutput.AdditionalContext; !strings.Contains(ctx, "your session id is") {
		t.Fatalf("mesh on after mesh off: %q", ctx)
	}
}
