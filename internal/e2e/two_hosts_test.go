package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
)

const lumio = "github.com/acme/lumio"

var bg = context.Background()

var joinLineRE = regexp.MustCompile(`(?s)agent-mesh join (\S+)\n.*paste this code when asked: (\S+)`)

// mesh is the user's two-machine setup: "ubuntu" runs the hub, "mac" joined it.
type mesh struct {
	url      string
	ubuntu   *machine
	mac      *machine
	onA, onB *client.Client // session tokens
	admin    *client.Client
}

// setUp drives the real onboarding commands in two fake homes.
func setUp(t *testing.T) *mesh {
	bin, tools := buildBinary(t), fakeTools(t)
	a, b := newMachine(t, bin, tools), newMachine(t, bin, tools)
	addr := freeAddr(t)
	url := "http://" + addr
	a.mustRun("up", "--no-service", "--name", "ubuntu", "--listen", addr)

	// Short session leases so the abandoned-task scenario runs in seconds.
	hubTOML := filepath.Join(a.cfg, "hub.toml")
	src, err := os.ReadFile(hubTOML)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(src), "retention = \"168h\"\n", "retention = \"168h\"\nsession_lease_ttl = \"2s\"\n", 1)
	if edited == string(src) {
		t.Fatal("hub.toml layout changed; cannot set session_lease_ttl")
	}
	if err := os.WriteFile(hubTOML, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	a.startHub(url)

	out := a.mustRun("invite", "mac")
	if strings.Contains(out, "not active yet") {
		t.Fatalf("invite did not reach the running hub:\n%s", out)
	}
	m := joinLineRE.FindStringSubmatch(out)
	if m == nil || m[1] != url {
		t.Fatalf("no join line for %s in:\n%s", url, out)
	}
	b.mustRun("join", m[1], m[2], "--no-service")
	return &mesh{url: url, ubuntu: a, mac: b, onA: a.sessions(url), onB: b.sessions(url),
		admin: client.New(url, a.token("admin.token"))}
}

func register(t *testing.T, c *client.Client, id, remote, wantRepo string) {
	t.Helper()
	s, err := c.RegisterSession(bg, api.RegisterSessionRequest{ID: id, Repo: remote})
	if err != nil || s.Repo != wantRepo {
		t.Fatalf("register %s: %+v %v", id, s, err)
	}
}

func TestTwoHostMesh(t *testing.T) {
	ms := setUp(t)
	onA, onB := ms.onA, ms.onB

	// 1) Sessions on both machines; different remote spellings, one project.
	register(t, onA, "a-boss", "https://github.com/acme/lumio.git", lumio)
	register(t, onA, "a-two", "github.com/acme/lumio", lumio)
	register(t, onB, "m-one", "git@github.com:acme/lumio.git", lumio)
	register(t, onB, "m-two", "ssh://git@GitHub.com:22/Acme/Lumio", lumio)
	register(t, onB, "m-other", "git@github.com:acme/other.git", "github.com/acme/other")

	t.Run("targeted tasks wake mac sessions and notify the creator", func(t *testing.T) { targeted(t, ms) })
	t.Run("untargeted task is claimed exactly once", func(t *testing.T) { untargeted(t, ms) })
	t.Run("cross-host messages are delivered at least once", func(t *testing.T) { messaging(t, ms) })
	t.Run("abandoned session task is reclaimed with a new fence", func(t *testing.T) { abandoned(t, ms) })
	t.Run("report works on both machines", func(t *testing.T) {
		for name, m := range map[string]*machine{"ubuntu": ms.ubuntu, "mac": ms.mac} {
			out, err := m.run("report")
			if err != nil || !strings.Contains(out, "# Morning report") || !strings.Contains(out, "## Done (4)") {
				t.Errorf("%s report: %v\n%s", name, err, out)
			}
		}
	})
	t.Run("doctor passes on both machines", func(t *testing.T) {
		for name, m := range map[string]*machine{"ubuntu": ms.ubuntu, "mac": ms.mac} {
			out, err := m.run("doctor")
			if err != nil {
				t.Errorf("%s doctor: %v\n%s", name, err, out)
			}
			for _, want := range []string{"✓ hub reachable", "✓ auth session", "✓ auth host"} {
				if !strings.Contains(out, want) {
					t.Errorf("%s doctor missing %q:\n%s", name, want, out)
				}
			}
		}
	})
}
