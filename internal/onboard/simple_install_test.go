package onboard

import (
	"context"
	"strings"
	"testing"
)

// The invite code carries the hub URL, so `join` needs nothing else.
func TestJoinWithOnlyTheCode(t *testing.T) {
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
	if !strings.Contains(hubOut.String(), "\n  agent-mesh join\n") {
		t.Errorf("invite does not print the bare join command:\n%s", hubOut)
	}
	code := joinLine(t, hubOut.String())[1]

	joiner, joinOut, _ := newTestEnv(t, "laptop")
	if err := Join(ctx, joiner, []string{code, "--no-service"}); err != nil {
		t.Fatalf("join <code>: %v\n%s", err, joinOut)
	}
	prompted, _, _ := newTestEnv(t, "laptop")
	prompted.In = strings.NewReader(code + "\n")
	if err := Join(ctx, prompted, nil); err != nil {
		t.Fatalf("join with the code on stdin: %v", err)
	}
	for _, e := range []*Env{joiner, prompted} {
		if !strings.Contains(readFile(t, e.Path("client.toml")), "http://"+listen) {
			t.Errorf("client.toml lacks the hub URL from the code")
		}
	}
}

// A first `up` with Tailscale connected listens on the tailnet address, so
// other machines can join without a second `up --listen`; a re-run keeps it.
func TestFirstUpListensOnTailnet(t *testing.T) {
	ctx := context.Background()
	e, out, _ := newTestEnv(t, "box")
	r := e.Runner.(*fakeRunner)
	r.bins["tailscale"] = true
	r.outs["/fake/bin/tailscale ip -4"] = "100.64.1.2\n"
	if err := Up(ctx, e, []string{"--no-service"}); err != nil {
		t.Fatalf("up: %v\n%s", err, out)
	}
	if !strings.Contains(readFile(t, e.Path("hub.toml")), `"100.64.1.2:47878"`) {
		t.Fatalf("hub.toml does not listen on the tailnet:\n%s", readFile(t, e.Path("hub.toml")))
	}
	r.outs["/fake/bin/tailscale ip -4"] = "100.64.9.9\n"
	if err := Up(ctx, e, []string{"--no-service"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, e.Path("hub.toml")), `"100.64.1.2:47878"`) {
		t.Error("re-run changed the listen address")
	}
}

// `daemon install` adds the optional daemon to a joined machine.
func TestDaemonInstall(t *testing.T) {
	ctx := context.Background()
	e, _, mgr := newTestEnv(t, "box")
	if err := DaemonInstall(ctx, e, nil); err == nil {
		t.Fatal("daemon install before up/join succeeded")
	}
	if err := Up(ctx, e, []string{"--no-service"}); err != nil {
		t.Fatal(err)
	}
	if err := DaemonInstall(ctx, e, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(mgr.installs, ","); got != "daemon" || !daemonInstalled(e) {
		t.Fatalf("installs = %s", got)
	}
}

// A re-run of up without --name keeps the name given the first time, even
// when it differs from the hostname.
func TestUpRerunKeepsTheFirstName(t *testing.T) {
	ctx := context.Background()
	e, out, _ := newTestEnv(t, "felix-box")
	if err := Up(ctx, e, []string{"--no-service", "--name", "ubuntu"}); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, e.Path("hub.toml"))
	if err := Up(ctx, e, []string{"--no-service"}); err != nil {
		t.Fatalf("re-run: %v\n%s", err, out)
	}
	if readFile(t, e.Path("hub.toml")) != before {
		t.Error("re-run without --name changed hub.toml")
	}
}
