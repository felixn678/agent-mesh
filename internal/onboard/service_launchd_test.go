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

// plistDoc is the subset of a launchd plist the tests inspect.
type plistDoc struct {
	strs map[string]string // top-level <key> -> <string> value
	args []string          // ProgramArguments
	env  map[string]string // EnvironmentVariables
	keys []string          // every <key>, in order
}

// parsePlist decodes the flat plist renderPlist emits (strict XML), pairing
// each top-level key with its string value and collecting the argv array and
// the environment dict.
func parsePlist(t *testing.T, p string) plistDoc {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(p))
	dec.Strict = true
	doc := plistDoc{strs: map[string]string{}, env: map[string]string{}}
	var key, envKey, elem string
	depth := 0 // nesting below the top-level <dict>
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return doc
		}
		if err != nil {
			t.Fatalf("invalid XML: %v\n%s", err, p)
		}
		switch v := tok.(type) {
		case xml.StartElement:
			elem = v.Name.Local
			if elem == "dict" || elem == "array" {
				depth++
			}
		case xml.EndElement:
			if v.Name.Local == "dict" || v.Name.Local == "array" {
				depth--
			}
			elem = ""
		case xml.CharData:
			text := string(v)
			switch {
			case elem == "key" && depth == 1:
				key = text
				doc.keys = append(doc.keys, text)
			case elem == "key" && depth == 2:
				envKey = text
			case elem == "string" && depth == 1:
				doc.strs[key] = text
			case elem == "string" && depth == 2 && key == "ProgramArguments":
				doc.args = append(doc.args, text)
			case elem == "string" && depth == 2 && key == "EnvironmentVariables":
				doc.env[envKey] = text
			}
		}
	}
}

// The rendered plist must be well-formed XML with the keys launchd needs,
// must escape XML metacharacters, must make the process write its own
// rotating --log-file, and must send stdout/stderr to a separate file that
// only collects startup failures.
func TestRenderPlistIsValidXML(t *testing.T) {
	home := "/Users/a&b"
	bin := home + "/.local/bin/agent-mesh"
	path := home + "/.local/bin:/opt/homebrew/bin:/usr/bin"
	doc := parsePlist(t, renderPlist(home, bin, path, "", Unit{Name: "hub", Args: []string{"hub", "--note=<x>"}}, "com.agentmesh.hub"))
	for _, k := range []string{"Label", "ProgramArguments", "EnvironmentVariables", "RunAtLoad",
		"KeepAlive", "ThrottleInterval", "Umask", "StandardOutPath", "StandardErrorPath"} {
		if !contains(doc.keys, k) {
			t.Errorf("missing key %s", k)
		}
	}
	logDir := filepath.Join(home, "Library", "Logs", "agent-mesh")
	wantArgs := []string{bin, "hub", "--note=<x>", "--log-file", filepath.Join(logDir, "hub.log")}
	if strings.Join(doc.args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Errorf("ProgramArguments %q, want %q", doc.args, wantArgs)
	}
	if doc.strs["Label"] != "com.agentmesh.hub" || doc.env["PATH"] != path {
		t.Errorf("label %q PATH %q", doc.strs["Label"], doc.env["PATH"])
	}
	stderrLog := filepath.Join(logDir, "hub.stderr.log")
	for _, k := range []string{"StandardOutPath", "StandardErrorPath"} {
		if doc.strs[k] != stderrLog {
			t.Errorf("%s = %q, want %q (must not be the rotating --log-file)", k, doc.strs[k], stderrLog)
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

// AGENTMESH_CONFIG_DIR set when up/join ran is handed to the services, so
// they read the same identity; unset, the units carry no such variable.
func TestUnitsCarryConfigDirOverrideOnlyWhenSet(t *testing.T) {
	u := Unit{Name: "daemon", Args: []string{"daemon"}, Desc: "d"}
	dir := "/Users/x/mesh b&c"
	doc := parsePlist(t, renderPlist("/Users/x", "/Users/x/bin/agent-mesh", "/usr/bin", dir, u, "com.agentmesh.daemon"))
	if doc.env["AGENTMESH_CONFIG_DIR"] != dir || doc.env["PATH"] != "/usr/bin" {
		t.Errorf("plist env %v", doc.env)
	}
	doc = parsePlist(t, renderPlist("/Users/x", "/Users/x/bin/agent-mesh", "/usr/bin", "", u, "com.agentmesh.daemon"))
	if _, ok := doc.env["AGENTMESH_CONFIG_DIR"]; ok {
		t.Errorf("plist env without override: %v", doc.env)
	}
	unit := renderSystemd("/b/agent-mesh", "/usr/bin", "/srv/mesh%a", u)
	if !strings.Contains(unit, "Environment=\"AGENTMESH_CONFIG_DIR=/srv/mesh%%a\"\n") {
		t.Errorf("systemd unit lacks escaped override:\n%s", unit)
	}
	if strings.Contains(renderSystemd("/b/agent-mesh", "/usr/bin", "", u), "AGENTMESH_CONFIG_DIR") {
		t.Error("systemd unit has override although none was set")
	}
}
