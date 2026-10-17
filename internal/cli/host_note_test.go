package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
)

func TestMachineProbeDetectsMobileToolchains(t *testing.T) {
	files := map[string]bool{
		"/Applications/Xcode.app":                         true,
		"/Users/u/Library/Android/sdk/platform-tools/adb": true,
		"/Users/u/Library/Android/sdk/emulator/emulator":  true,
		"/Applications/Google Chrome.app":                 true,
	}
	path := map[string]bool{"node": true, "pod": true}
	p := machineProbe{goos: "darwin", goarch: "arm64", home: "/Users/u",
		lookPath: func(n string) (string, error) {
			if path[n] {
				return "/usr/local/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
		exists: func(f string) bool { return files[f] }, getenv: func(string) string { return "" }}
	m := p.detectMachine()
	if m.OS != "macos/arm64" || strings.Join(m.Tools, ",") != "xcode,adb,android-emulator,chrome,node,pod" {
		t.Fatalf("mac: %+v", m)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	// Linux: an Xcode-looking path never counts, the SDK comes from ANDROID_HOME.
	p.goos, p.goarch, p.home = "linux", "amd64", "/home/u"
	files = map[string]bool{"/Applications/Xcode.app": true, "/opt/sdk/platform-tools/adb": true}
	p.exists = func(f string) bool { return files[f] }
	p.getenv = func(k string) string {
		if k == "ANDROID_HOME" {
			return "/opt/sdk"
		}
		return ""
	}
	path = map[string]bool{"go": true, "docker": true}
	if m := p.detectMachine(); m.OS != "linux/amd64" || strings.Join(m.Tools, ",") != "adb,go,docker" {
		t.Fatalf("linux: %+v", m)
	}
}

func TestHostNoteCommandAndSessionBriefing(t *testing.T) {
	h := startHub(t)
	testEnv(t, h) // session.token of host-a
	if out, _, err := runCLI(t, "", "host-note", "frontend", "web,", "React", "Native/Expo"); err != nil ||
		!strings.Contains(out, `host-a: "frontend web, React Native/Expo"`) {
		t.Fatalf("set own note: %q %v", out, err)
	}
	if _, _, err := runCLI(t, "", "host-note", "--host", "host-b", "backend"); err == nil ||
		!strings.Contains(err.Error(), "host-note --host needs the admin token") {
		t.Fatalf("other machine with a session token: %v", err)
	}
	if err := h.Admin().SetHostNote(bg, "host-b", "backend, API, web frontend"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Session("host-b").RegisterSession(bg, api.RegisterSessionRequest{ID: "peer-0001",
		Machine: &api.MachineInfo{OS: "linux/amd64", Tools: []string{"docker"}}}); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "", "host-note")
	if err != nil || !strings.Contains(out, `host-b (linux/amd64, 1 live session) note: "backend, API, web frontend" tools: docker`) {
		t.Fatalf("list: %q %v", out, err)
	}
	if out, _, _ := runCLI(t, "", "hosts"); !strings.Contains(out, "machines (set a role with `agent-mesh host-note`):") {
		t.Fatalf("hosts: %q", out)
	}

	ho, _ := hook(t, "session-start", "hook_session_start.json")
	ctx := ho.HookSpecificOutput.AdditionalContext
	for _, want := range []string{"machines in the mesh", "host-a [this machine] (", `note: "frontend web, React Native/Expo"`,
		`host-b (linux/amd64, 1 live session) note: "backend, API, web frontend"`, "task_add(target_host=<machine>"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("briefing lacks %q:\n%s", want, ctx)
		}
	}
	// The hook reported this machine's OS.
	list, _ := h.Store.ListHostProfiles(bg)
	if list[0].Name != "host-a" || list[0].OS == "" {
		t.Fatalf("own profile: %+v", list)
	}
	if out, _, err := runCLI(t, "", "host-note", "--clear"); err != nil || !strings.Contains(out, "cleared the note of host-a") {
		t.Fatalf("clear: %q %v", out, err)
	}
}

// A single machine needs no routing briefing.
func TestSessionStartWithoutPeersHasNoBriefing(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	ho, _ := hook(t, "session-start", "hook_session_start.json")
	if strings.Contains(ho.HookSpecificOutput.AdditionalContext, "machines in the mesh") {
		t.Fatalf("briefing with one machine: %s", ho.HookSpecificOutput.AdditionalContext)
	}
}
