package onboard

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestBundleRoundTripAndValidation(t *testing.T) {
	in := Bundle{V: 1, URL: "http://100.64.0.1:8787", Host: "laptop", HostToken: strings.Repeat("a", 64), SessionToken: strings.Repeat("b", 64)}
	enc, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(enc, "=+/ \n") {
		t.Errorf("bundle not paste-safe base64url: %q", enc)
	}
	out, err := DecodeBundle(enc)
	if err != nil || out != in {
		t.Fatalf("round trip: %+v %v", out, err)
	}
	for name, mutate := range map[string]func(*Bundle){
		"version":   func(b *Bundle) { b.V = 2 },
		"host":      func(b *Bundle) { b.Host = "Bad Host" },
		"token":     func(b *Bundle) { b.HostToken = "short" },
		"url":       func(b *Bundle) { b.URL = "ftp://x" },
		"url creds": func(b *Bundle) { b.URL = "http://u:p@h:1" },
	} {
		b := in
		mutate(&b)
		e, _ := b.Encode()
		if _, err := DecodeBundle(e); err == nil {
			t.Errorf("%s: invalid bundle accepted", name)
		}
	}
	if _, err := DecodeBundle("%%%"); err == nil {
		t.Error("garbage accepted")
	}
}

func TestAddToTablePreservesContent(t *testing.T) {
	src := "# top comment\nlisten = \"127.0.0.1:8787\"\n\n# hosts\n[host_tokens]\n\"a\" = \"sha256:1\"   # keep\n\n# sessions\n[session_tokens]\n\"a\" = \"sha256:2\"\n"
	got := addToTable(src, "host_tokens", `"b" = "sha256:3"`)
	want := "# top comment\nlisten = \"127.0.0.1:8787\"\n\n# hosts\n[host_tokens]\n\"a\" = \"sha256:1\"   # keep\n\"b\" = \"sha256:3\"\n\n# sessions\n[session_tokens]\n\"a\" = \"sha256:2\"\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	got = addToTable(got, "session_tokens", `"b" = "sha256:4"`)
	if !strings.HasSuffix(got, "\"a\" = \"sha256:2\"\n\"b\" = \"sha256:4\"\n") {
		t.Errorf("append at EOF failed:\n%s", got)
	}
	if got := addToTable("x = 1\n", "new", `"k" = "v"`); got != "x = 1\n\n[new]\n\"k\" = \"v\"\n" {
		t.Errorf("missing table: %q", got)
	}
}

func TestSetTopLevelKeepsComment(t *testing.T) {
	src := "listen = \"127.0.0.1:8787\"   # note\n[t]\nlisten = \"keep\"\n"
	got, ok := setTopLevel(src, "listen", "100.1.1.1:8787")
	if !ok || got != "listen = \"100.1.1.1:8787\"   # note\n[t]\nlisten = \"keep\"\n" {
		t.Errorf("got %q %v", got, ok)
	}
}

func TestSystemdInstallIsIdempotent(t *testing.T) {
	e, _, _ := newTestEnv(t, "box")
	r := newFakeRunner()
	for _, k := range []string{"daemon-reload", "enable --now agent-mesh-daemon.service", "restart agent-mesh-daemon.service"} {
		r.outs["systemctl --user "+k] = ""
	}
	e.Runner = r
	m := &systemdManager{e: e}
	u := Unit{Name: "daemon", Args: []string{"daemon"}, Desc: "d"}
	if err := m.Install(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	path := e.Home + "/.config/systemd/user/agent-mesh-daemon.service"
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "ExecStart=/fake/bin/agent-mesh daemon") {
		t.Fatalf("unit: %v %s", err, body)
	}
	n := len(r.calls)
	if err := m.Install(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls[n:] {
		if strings.Contains(c, "restart") || strings.Contains(c, "daemon-reload") {
			t.Errorf("unchanged unit triggered %q", c)
		}
	}
	e.Binary = "/path with space/agent-mesh"
	if err := m.Install(context.Background(), u); err == nil {
		t.Error("binary path with spaces accepted")
	}
}

func TestRenderPlist(t *testing.T) {
	p := renderPlist("/Users/x", "/Users/x/bin/agent-mesh", Unit{Name: "daemon", Args: []string{"daemon"}}, "com.agentmesh.daemon")
	for _, want := range []string{"<string>com.agentmesh.daemon</string>", "<string>/Users/x/bin/agent-mesh</string>", "<integer>63</integer>", "<key>KeepAlive</key>"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %s", want)
		}
	}
}

func TestShortHostName(t *testing.T) {
	e, _, _ := newTestEnv(t, "My_Mac.local")
	got, err := e.ShortHostName()
	if err != nil || got != "my-mac" {
		t.Errorf("got %q %v", got, err)
	}
}

func readDirNames(dir string) ([]string, error) {
	es, err := os.ReadDir(dir)
	var n []string
	for _, e := range es {
		n = append(n, e.Name())
	}
	return n, err
}

func chmod(p string, m os.FileMode) error { return os.Chmod(p, m) }
