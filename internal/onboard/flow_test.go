package onboard

import (
	"context"
	"github.com/felixn678/agent-mesh/internal/hub"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpInviteJoinDoctorEndToEnd(t *testing.T) {
	ctx := context.Background()
	hubEnv, hubOut, hubMgr := newTestEnv(t, "Hubbox.local")
	useHome(t, hubEnv)
	listen := freeAddr(t)

	if err := Up(ctx, hubEnv, []string{"--listen", listen}); err != nil {
		t.Fatalf("up: %v\n%s", err, hubOut)
	}
	for _, p := range []string{"admin.token", "host.token", "session.token", "hub.toml", "daemon.toml", "client.toml"} {
		if m := mode(t, hubEnv.Path(p)); m != 0o600 {
			t.Errorf("%s mode %v, want 0600", p, m)
		}
	}
	if m := mode(t, hubEnv.ConfigDir); m != 0o700 {
		t.Errorf("config dir mode %v, want 0700", m)
	}
	if !strings.Contains(readFile(t, hubEnv.Path("hub.toml")), `"hubbox" =`) {
		t.Error("hub.toml lacks default short host name")
	}
	if got := strings.Join(hubMgr.installs, ","); got != "hub,daemon" {
		t.Errorf("installs = %s", got)
	}
	if err := Doctor(ctx, hubEnv, nil); err != nil {
		t.Fatalf("hub doctor: %v\n%s", err, hubOut)
	}

	hubOut.Reset()
	if err := Invite(ctx, hubEnv, []string{"laptop"}); err != nil {
		t.Fatalf("invite: %v\n%s", err, hubOut)
	}
	if hubMgr.reloads != 1 {
		t.Errorf("reloads = %d, want 1", hubMgr.reloads)
	}
	if !strings.Contains(hubOut.String(), "is a secret") {
		t.Error("invite output lacks secrets warning")
	}
	arg := joinLine(t, hubOut.String())
	if arg[0] != "http://"+listen {
		t.Errorf("join url = %s", arg[0])
	}

	// Second machine: separate fake home.
	joiner, joinOut, joinMgr := newTestEnv(t, "laptop")
	if err := Join(ctx, joiner, arg); err != nil {
		t.Fatalf("join: %v\n%s", err, joinOut)
	}
	for _, p := range []string{"host.token", "session.token", "daemon.toml", "client.toml"} {
		if m := mode(t, joiner.Path(p)); m != 0o600 {
			t.Errorf("joiner %s mode %v, want 0600", p, m)
		}
	}
	if m := mode(t, joiner.ConfigDir); m != 0o700 {
		t.Errorf("joiner config dir mode %v", m)
	}
	if !strings.Contains(readFile(t, joiner.Path("daemon.toml")), "http://"+listen) {
		t.Error("daemon.toml lacks hub url")
	}
	if strings.Join(joinMgr.installs, ",") != "daemon" || !strings.Contains(joinOut.String(), "setup-claude") {
		t.Errorf("join installs=%v out=%s", joinMgr.installs, joinOut)
	}
	joinOut.Reset()
	if err := Doctor(ctx, joiner, nil); err != nil {
		t.Fatalf("joiner doctor: %v\n%s", err, joinOut)
	}
	// Re-joining with the same invite is a no-op, not an error.
	if err := Join(ctx, joiner, arg); err != nil {
		t.Fatalf("rejoin: %v", err)
	}
}

func TestUpIsIdempotent(t *testing.T) {
	ctx := context.Background()
	e, out, mgr := newTestEnv(t, "box")
	useHome(t, e)
	listen := freeAddr(t)
	if err := Up(ctx, e, []string{"--listen", listen, "--name", "box"}); err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}
	files := []string{"admin.token", "host.token", "session.token", "hub.toml", "daemon.toml", "client.toml"}
	before := map[string]string{}
	for _, p := range files {
		before[p] = readFile(t, e.Path(p))
	}
	if err := Up(ctx, e, []string{"--name", "box"}); err != nil { // no --listen: keeps the existing one
		t.Fatalf("second up: %v\n%s", err, out)
	}
	for _, p := range files {
		if readFile(t, e.Path(p)) != before[p] {
			t.Errorf("%s changed on re-run", p)
		}
	}
	if len(mgr.restarts) != 0 {
		t.Errorf("restarts = %v, want none", mgr.restarts)
	}
}

func TestUpRejectsWildcardListen(t *testing.T) {
	e, _, _ := newTestEnv(t, "box")
	for _, l := range []string{"0.0.0.0:47878", ":47878", "[::]:47878", "nonsense"} {
		if err := Up(context.Background(), e, []string{"--listen", l, "--no-service"}); err == nil {
			t.Errorf("listen %q accepted", l)
		}
	}
}

func TestInviteReloadsViaPidfileWhenNoService(t *testing.T) {
	ctx := context.Background()
	e, out, mgr := newTestEnv(t, "box")
	useHome(t, e)
	if err := Up(ctx, e, []string{"--listen", freeAddr(t)}); err != nil {
		t.Fatal(err)
	}
	mgr.running = false     // service manager does not own the hub
	orig := hub.ProcessArgs // the in-process hub is the test binary; pose as the real thing
	hub.ProcessArgs = func(int) ([]string, error) { return []string{"/bin/agent-mesh", "hub"}, nil }
	t.Cleanup(func() { hub.ProcessArgs = orig })
	out.Reset()
	if err := Invite(ctx, e, []string{"other"}); err != nil {
		t.Fatalf("invite: %v\n%s", err, out)
	}
	if mgr.reloads != 0 || strings.Contains(out.String(), "not active yet") {
		t.Errorf("reloads=%d out=%s", mgr.reloads, out)
	}
}

func TestInviteRejectsDuplicateAndBadName(t *testing.T) {
	ctx := context.Background()
	e, _, _ := newTestEnv(t, "box")
	useHome(t, e)
	if err := Up(ctx, e, []string{"--listen", freeAddr(t), "--no-service"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"box", "Bad Name", "admin"} {
		if err := Invite(ctx, e, []string{name}); err == nil {
			t.Errorf("invite %q accepted", name)
		}
	}
}

func TestJoinWithBadTokenWritesNothing(t *testing.T) {
	ctx := context.Background()
	hubEnv, _, _ := newTestEnv(t, "box")
	useHome(t, hubEnv)
	listen := freeAddr(t)
	if err := Up(ctx, hubEnv, []string{"--listen", listen}); err != nil {
		t.Fatal(err)
	}
	enc, _ := Bundle{V: 1, URL: "http://" + listen, Host: "evil", HostToken: strings.Repeat("a", 64), SessionToken: strings.Repeat("b", 64)}.Encode()
	j, _, _ := newTestEnv(t, "evil")
	if err := Join(ctx, j, []string{"http://" + listen, enc}); err == nil {
		t.Fatal("join with unknown tokens succeeded")
	}
	if _, err := readDirNames(j.ConfigDir); err == nil {
		t.Error("config dir created despite failed join")
	}
}

func TestDoctorFailsOnLooseTokenAndMissingTool(t *testing.T) {
	ctx := context.Background()
	e, out, _ := newTestEnv(t, "box")
	useHome(t, e)
	if err := Up(ctx, e, []string{"--listen", freeAddr(t)}); err != nil {
		t.Fatal(err)
	}
	if err := chmod(e.Path("host.token"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.Runner.(*fakeRunner).bins["gitleaks"] = false
	out.Reset()
	if err := Doctor(ctx, e, nil); err == nil {
		t.Fatalf("doctor passed:\n%s", out)
	}
	for _, want := range []string{"✗ host.token", "✗ gitleaks", "fix:", "✓ auth session"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
}

func TestJoinReadsInviteFromStdin(t *testing.T) {
	ctx := context.Background()
	hubEnv, hubOut, _ := newTestEnv(t, "box")
	useHome(t, hubEnv)
	listen := freeAddr(t)
	if err := Up(ctx, hubEnv, []string{"--listen", listen, "--name", "box"}); err != nil {
		t.Fatal(err)
	}
	if err := Invite(ctx, hubEnv, []string{"laptop"}); err != nil {
		t.Fatalf("invite: %v\n%s", err, hubOut)
	}
	arg := joinLine(t, hubOut.String())
	for _, form := range [][]string{{arg[0]}, {arg[0], "-"}} {
		joiner, joinOut, _ := newTestEnv(t, "laptop")
		joiner.In = strings.NewReader(arg[1] + "\n")
		if err := Join(ctx, joiner, append(form, "--no-service")); err != nil {
			t.Fatalf("join %v: %v\n%s", form, err, joinOut)
		}
		if !strings.Contains(readFile(t, joiner.Path("daemon.toml")), "http://"+listen) {
			t.Errorf("join %v did not write daemon.toml", form)
		}
	}
	empty, _, _ := newTestEnv(t, "laptop")
	empty.In = strings.NewReader("")
	if err := Join(ctx, empty, []string{arg[0]}); err == nil {
		t.Error("join with empty stdin succeeded")
	}
}

func TestDoctorRejectsSwappedTokens(t *testing.T) {
	ctx := context.Background()
	e, out, _ := newTestEnv(t, "box")
	useHome(t, e)
	if err := Up(ctx, e, []string{"--listen", freeAddr(t), "--name", "box"}); err != nil {
		t.Fatal(err)
	}
	host, sess := readFile(t, e.Path("host.token")), readFile(t, e.Path("session.token"))
	if err := os.WriteFile(e.Path("host.token"), []byte(sess), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.Path("session.token"), []byte(host), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Doctor(ctx, e, nil); err == nil || !strings.Contains(out.String(), "session token but a host token is required") {
		t.Fatalf("swapped tokens passed doctor: %v\n%s", err, out)
	}
}

func TestUpListenChangeTellsToRerunSetupClaude(t *testing.T) {
	ctx := context.Background()
	e, out, _ := newTestEnv(t, "box")
	useHome(t, e)
	if err := Up(ctx, e, []string{"--no-service", "--name", "box"}); err != nil {
		t.Fatal(err)
	}
	listen := freeAddr(t)
	out.Reset()
	if err := Up(ctx, e, []string{"--no-service", "--name", "box", "--listen", listen}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "agent-mesh setup-claude") || !strings.Contains(readFile(t, e.Path("client.toml")), "http://"+listen) {
		t.Errorf("client.toml/hint not updated:\n%s", out)
	}
}

func TestServicePathAndDoctorWarning(t *testing.T) {
	e, _, _ := newTestEnv(t, "box")
	r := newFakeRunner()
	r.paths = map[string]string{"claude": "/opt/nvm/bin/claude", "git": "/usr/bin/git"}
	e.Runner = r
	path := e.servicePath()
	if !strings.HasPrefix(path, "/opt/nvm/bin:/usr/bin:") || !strings.Contains(path, "/usr/local/bin") {
		t.Fatalf("service path = %s", path)
	}
	unit := renderSystemd(e.Binary, path, "", Unit{Name: "daemon", Args: []string{"daemon"}})
	dir := filepath.Join(e.Home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-mesh-daemon.service"), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := servicePathCheck(e); res.Status != Pass {
		t.Errorf("good unit: %+v", res)
	}
	r.paths["claude"] = "/elsewhere/claude"
	if res := servicePathCheck(e); res.Status != Warn || !strings.Contains(res.Detail, "/elsewhere") {
		t.Errorf("stale unit: %+v", res)
	}
}
