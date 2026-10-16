package cli

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

// gitRepoCwd makes a temp git repo with an origin remote and returns its path.
func gitRepoCwd(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func hookStdin(cwd, event string, stopActive bool) string {
	return `{"session_id":"` + fixtureSession + `","cwd":"` + cwd + `","hook_event_name":"` + event + `","stop_hook_active":` +
		map[bool]string{true: "true", false: "false"}[stopActive] + `}`
}

func TestSessionStartSendsGitRemoteAsRepo(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	cwd := gitRepoCwd(t, "git@github.com:Owner/Proj.git")
	out, _, err := runCLI(t, hookStdin(cwd, "SessionStart", false), "hook", "session-start")
	if err != nil || !strings.Contains(out, "task_claim") {
		t.Fatalf("%q %v", out, err)
	}
	list, _ := h.Store.ListSessions(bg, true)
	if len(list) != 1 || list[0].Repo != "github.com/owner/proj" {
		t.Fatalf("sessions: %+v", list)
	}
	// Later prompts send no repo and keep the stored one; a cwd without a
	// remote registers fine with no repo.
	runCLI(t, hookStdin(t.TempDir(), "UserPromptSubmit", false), "hook", "prompt")
	if list, _ = h.Store.ListSessions(bg, true); list[0].Repo != "github.com/owner/proj" {
		t.Fatalf("repo lost: %+v", list)
	}
	if gitRemote(bg, t.TempDir()) != "" || gitRemote(bg, gitRepoCwd(t, "not a url")) != "" {
		t.Fatal("non-repo cwd must yield no remote")
	}
}

func TestStopHookHeartbeatsHeldTask(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	cwd := gitRepoCwd(t, "https://github.com/owner/proj")
	runCLI(t, hookStdin(cwd, "SessionStart", false), "hook", "session-start")
	if _, err := h.Admin().AddTask(bg, api.TaskSpec{Repo: "github.com/owner/proj", Prompt: "p", MaxBudgetUSD: 1}); err != nil {
		t.Fatal(err)
	}
	c, err := h.Session("host-a").SessionClaim(bg, fixtureSession)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := h.Store.Get(bg, c.ID)
	h.Clock.Advance(10 * time.Minute)
	runCLI(t, hookStdin(cwd, "Stop", true), "hook", "stop")
	after, _ := h.Store.Get(bg, c.ID)
	if after.LeaseExpiresAt <= before.LeaseExpiresAt {
		t.Fatalf("lease not extended: %d -> %d", before.LeaseExpiresAt, after.LeaseExpiresAt)
	}
}

func TestWaiterWakesOnceForClaimableWork(t *testing.T) {
	h := startHub(t)
	testEnv(t, h)
	cwd := gitRepoCwd(t, "https://github.com/owner/proj")
	runCLI(t, hookStdin(cwd, "SessionStart", false), "hook", "session-start")
	done, _ := startWaiter(t, bg, fastWait)
	waitFor(t, func() bool { return waiterAlive(waitersDir(), fixtureSession) })
	if _, err := h.Session("host-b").AddTask(bg, api.TaskSpec{Repo: "https://github.com/owner/proj.git", Prompt: "p", MaxBudgetUSD: 1, TargetHost: "host-a"}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || !strings.Contains(r.out, "1 task(s) can be claimed") || !strings.Contains(r.out, "task_claim(session="+fixtureSession+")") {
			t.Fatalf("%q %v", r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not wake for claimable work")
	}
	// Restarted without claiming: the same work does not wake it again...
	done, _ = startWaiter(t, bg, waitOpts{maxDur: 2 * time.Second, poll: time.Second, backoffMin: time.Millisecond, backoffMax: time.Millisecond})
	if r := <-done; r.err != nil || !strings.Contains(r.out, "no messages in 2s") {
		t.Fatalf("re-woke for announced work: %q %v", r.out, r.err)
	}
	// ...but a message still does, and new work does.
	done, _ = startWaiter(t, bg, fastWait)
	waitFor(t, func() bool { return waiterAlive(waitersDir(), fixtureSession) })
	h.Session("host-b").AddTask(bg, api.TaskSpec{Repo: "github.com/owner/proj", Prompt: "q", MaxBudgetUSD: 1})
	select {
	case r := <-done:
		if !strings.Contains(r.out, "2 task(s) can be claimed") {
			t.Fatalf("%q %v", r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("new work did not wake the waiter")
	}
}
