package onboard

import (
	"context"
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
	if !strings.Contains(hubOut.String(), "contains secrets") {
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
	for _, l := range []string{"0.0.0.0:8787", ":8787", "[::]:8787", "nonsense"} {
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
	mgr.running = false // service manager does not own the hub
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
