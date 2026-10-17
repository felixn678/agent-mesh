package onboard

import (
	"context"
	"strings"
	"testing"
)

// A lost invite code is replaced with --reissue: the old code and tokens stop
// working, the new code joins, and the hub machine itself is never reissued.
func TestInviteReissueReplacesTokens(t *testing.T) {
	ctx := context.Background()
	hubEnv, hubOut, _ := newTestEnv(t, "box")
	useHome(t, hubEnv)
	if err := Up(ctx, hubEnv, []string{"--listen", freeAddr(t)}); err != nil {
		t.Fatalf("up: %v\n%s", err, hubOut)
	}
	hubOut.Reset()
	if err := Invite(ctx, hubEnv, []string{"laptop"}); err != nil {
		t.Fatal(err)
	}
	oldArg := joinLine(t, hubOut.String())

	err := Invite(ctx, hubEnv, []string{"laptop"})
	if err == nil || !strings.Contains(err.Error(), "agent-mesh invite laptop --reissue") {
		t.Fatalf("duplicate invite: %v", err)
	}
	hubOut.Reset()
	if err := Invite(ctx, hubEnv, []string{"laptop", "--reissue"}); err != nil {
		t.Fatalf("reissue: %v\n%s", err, hubOut)
	}
	newArg := joinLine(t, hubOut.String())
	toml := readFile(t, hubEnv.Path("hub.toml"))
	if strings.Count(toml, `"laptop" =`) != 2 { // one host token, one session token
		t.Fatalf("hub.toml after reissue:\n%s", toml)
	}

	joiner, joinOut, _ := newTestEnv(t, "laptop")
	if err := Join(ctx, joiner, oldArg); err == nil {
		t.Fatal("old invite code still joins after reissue")
	}
	if err := Join(ctx, joiner, newArg); err != nil {
		t.Fatalf("join with new code: %v\n%s", err, joinOut)
	}

	for args, want := range map[string]string{"box --reissue": "this hub machine", "ghost --reissue": "no tokens to reissue"} {
		if err := Invite(ctx, hubEnv, strings.Fields(args)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("invite %s: %v", args, err)
		}
	}
}

func TestRemoveFromTableKeepsOtherLines(t *testing.T) {
	src := "listen = \"x\"\n[host_tokens]\n\"a\" = \"1\"   # keep\nlaptop = \"2\"\n\n[session_tokens]\n\"laptop\" = \"3\"\n\"b\" = \"4\"\n"
	got := removeFromTable(removeFromTable(src, "host_tokens", "laptop"), "session_tokens", "laptop")
	want := "listen = \"x\"\n[host_tokens]\n\"a\" = \"1\"   # keep\n\n[session_tokens]\n\"b\" = \"4\"\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUnreachableHintNamesTailscale(t *testing.T) {
	if h := unreachableHint("http://100.94.1.2:47878"); !strings.Contains(h, "Tailscale") {
		t.Errorf("tailnet hint: %s", h)
	}
	if h := unreachableHint("http://192.0.2.10:47878"); strings.Contains(h, "Tailscale") || !strings.Contains(h, "agent-mesh doctor") {
		t.Errorf("generic hint: %s", h)
	}
}
