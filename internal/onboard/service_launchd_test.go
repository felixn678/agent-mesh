package onboard

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// launchctlScript answers launchctl invocations by subcommand and records
// every call, so the manager's decisions can be checked without a Mac.
type launchctlScript struct {
	calls       []string
	domainOK    bool
	loaded      bool
	running     bool
	failBoot    int // bootstrap fails this many times before succeeding
	bootoutFail bool
}

func (s *launchctlScript) LookPath(name string) (string, error) { return "/usr/bin/" + name, nil }

func (s *launchctlScript) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "launchctl" {
		return nil, errors.New("unexpected " + name)
	}
	s.calls = append(s.calls, strings.Join(args, " "))
	switch args[0] {
	case "print":
		if !strings.Contains(args[1], "/com.agentmesh.") { // the domain itself
			if s.domainOK {
				return []byte("gui domain"), nil
			}
			return []byte("Bad request."), errors.New("exit status 113")
		}
		if !s.loaded {
			return []byte("Could not find service"), errors.New("exit status 113")
		}
		if s.running {
			return []byte("\tstate = running\n"), nil
		}
		return []byte("\tstate = not running\n"), nil
	case "bootstrap":
		if s.failBoot > 0 {
			s.failBoot--
			return []byte("Bootstrap failed: 5: Input/output error"), errors.New("exit status 5")
		}
		s.loaded, s.running = true, true
	case "bootout":
		if s.bootoutFail {
			return nil, errors.New("exit status 3")
		}
		s.loaded, s.running = false, false
	case "kickstart":
		s.running = true
	}
	return nil, nil
}

func newLaunchdTest(t *testing.T, s *launchctlScript) (*launchdManager, Unit) {
	t.Helper()
	old := launchdRetryDelay
	launchdRetryDelay = 0
	t.Cleanup(func() { launchdRetryDelay = old })
	e := &Env{Home: t.TempDir(), Binary: "/Users/x/.local/bin/agent-mesh", GOOS: "darwin", Out: io.Discard, Runner: s}
	return &launchdManager{e: e}, Unit{Name: "daemon", Args: []string{"daemon"}}
}

func verbs(calls []string) string {
	var v []string
	for _, c := range calls {
		f := strings.Fields(c)
		if f[0] == "print" {
			continue
		}
		v = append(v, f[0])
	}
	return strings.Join(v, ",")
}

func TestLaunchdInstallFreshBootstraps(t *testing.T) {
	s := &launchctlScript{domainOK: true}
	m, u := newLaunchdTest(t, s)
	if err := m.Install(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if got := verbs(s.calls); got != "bootstrap" {
		t.Fatalf("verbs %q", got)
	}
	uid := strconv.Itoa(os.Getuid())
	want := "bootstrap gui/" + uid + " " + m.plistPath("daemon")
	if s.calls[len(s.calls)-1] != want {
		t.Errorf("call %q, want %q", s.calls[len(s.calls)-1], want)
	}
	if fi, err := os.Stat(m.plistPath("daemon")); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("plist mode: %v %v", fi, err)
	}
}

func TestLaunchdInstallUnchangedRunningIsNoop(t *testing.T) {
	s := &launchctlScript{domainOK: true}
	m, u := newLaunchdTest(t, s)
	if err := m.Install(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	s.calls = nil
	if err := m.Install(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if got := verbs(s.calls); got != "" {
		t.Errorf("re-install ran %q", got)
	}
}

// A loaded job that is not running (crashed and throttled, or stopped) must be
// started with kickstart: bootstrapping an already loaded job fails with 5.
func TestLaunchdInstallLoadedStoppedKickstarts(t *testing.T) {
	s := &launchctlScript{domainOK: true}
	m, u := newLaunchdTest(t, s)
	if err := m.Install(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	s.running, s.calls = false, nil
	if err := m.Install(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if got := verbs(s.calls); got != "kickstart" {
		t.Errorf("verbs %q, want kickstart", got)
	}
}

// A changed definition is booted out and bootstrapped again, retrying while
// launchd still tears the old job down.
func TestLaunchdInstallChangedReloadsWithRetry(t *testing.T) {
	s := &launchctlScript{domainOK: true}
	m, u := newLaunchdTest(t, s)
	if err := m.Install(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	m.e.Binary = "/Users/x/bin/agent-mesh" // definition changes
	s.calls, s.failBoot = nil, 2
	if err := m.Install(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if got := verbs(s.calls); got != "bootout,bootstrap,bootstrap,bootstrap" {
		t.Errorf("verbs %q", got)
	}
	b, _ := os.ReadFile(m.plistPath("daemon"))
	if !bytes.Contains(b, []byte("<string>/Users/x/bin/agent-mesh</string>")) {
		t.Error("plist not rewritten")
	}
}

func TestLaunchdInstallGivesUpAfterRetries(t *testing.T) {
	s := &launchctlScript{domainOK: true, failBoot: launchdBootstrapAttempts}
	m, u := newLaunchdTest(t, s)
	err := m.Install(context.Background(), u)
	if err == nil || !strings.Contains(err.Error(), "Input/output error") {
		t.Fatalf("err %v", err)
	}
}

func TestLaunchdInstallWithoutGUIDomainExplains(t *testing.T) {
	s := &launchctlScript{domainOK: false}
	m, u := newLaunchdTest(t, s)
	err := m.Install(context.Background(), u)
	if err == nil || !strings.Contains(err.Error(), "--no-service") {
		t.Fatalf("err %v", err)
	}
	if _, statErr := os.Stat(m.plistPath("daemon")); statErr == nil {
		t.Error("plist written although launchd cannot load it")
	}
}

// The rendered plist must be well-formed XML with the keys launchd needs,
// and must escape XML metacharacters in paths and arguments.
func TestRenderPlistIsValidXML(t *testing.T) {
	home := "/Users/a&b"
	p := renderPlist(home, home+"/.local/bin/agent-mesh", home+"/.local/bin:/opt/homebrew/bin:/usr/bin",
		Unit{Name: "hub", Args: []string{"hub", "--note=<x>"}}, "com.agentmesh.hub")
	dec := xml.NewDecoder(strings.NewReader(p))
	dec.Strict = true
	var keys, strs []string
	var cur *[]string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid XML: %v\n%s", err, p)
		}
		switch v := tok.(type) {
		case xml.StartElement:
			cur = nil
			switch v.Name.Local {
			case "key":
				cur = &keys
			case "string":
				cur = &strs
			}
		case xml.CharData:
			if cur != nil {
				*cur = append(*cur, string(v))
				cur = nil
			}
		}
	}
	for _, k := range []string{"Label", "ProgramArguments", "EnvironmentVariables", "PATH", "RunAtLoad",
		"KeepAlive", "ThrottleInterval", "Umask", "StandardOutPath", "StandardErrorPath"} {
		if !contains(keys, k) {
			t.Errorf("missing key %s", k)
		}
	}
	for _, s := range []string{"com.agentmesh.hub", home + "/.local/bin/agent-mesh", "hub", "--note=<x>",
		filepath.Join(home, "Library", "Logs", "agent-mesh", "hub.log")} {
		if !contains(strs, s) {
			t.Errorf("missing string %q in %v", s, strs)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
