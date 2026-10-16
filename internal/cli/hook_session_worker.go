package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/store"
)

// gitRemote returns `git -C cwd remote get-url origin` if it normalizes to a
// repo key, else "" (fail-soft: a session without a remote still messages,
// it just cannot claim repo tasks).
func gitRemote(ctx context.Context, cwd string) string {
	if cwd == "" {
		return ""
	}
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	remote := strings.TrimSpace(string(out))
	if _, err := api.NormalizeRepo(remote); err != nil {
		return ""
	}
	return remote
}

// heartbeatHeld extends the lease of every task this session holds, so a
// long interactive task does not expire between turns. Best effort.
func heartbeatHeld(ctx context.Context, c *client.Client, session string) error {
	tasks, err := c.ListTasks(ctx, api.StateRunning, time.Time{})
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.ClaimedBy == store.SessionHolderPrefix+session {
			if err := c.SessionHeartbeat(ctx, session, t.ID, api.HeartbeatRequest{Fence: t.Fence}); err != nil {
				return err
			}
		}
	}
	return nil
}

// claimCooldown keeps a waiter from re-waking a session for the same
// claimable work it already announced (the session may be busy or have
// decided not to claim); new work or the cooldown expiring wakes it again.
const claimCooldown = 10 * time.Minute

func claimMarkPath(dir, id string) string { return filepath.Join(dir, id+".claimable") }

// claimWakeDue reports whether n claimable tasks should wake the session.
func claimWakeDue(dir, id string, n int) bool {
	if n <= 0 {
		_ = os.Remove(claimMarkPath(dir, id))
		return false
	}
	st, err := os.Stat(claimMarkPath(dir, id))
	if err != nil || time.Since(st.ModTime()) > claimCooldown {
		return true
	}
	b, _ := os.ReadFile(claimMarkPath(dir, id))
	prev, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n > prev
}

func markClaimWake(dir, id string, n int) {
	_ = os.WriteFile(claimMarkPath(dir, id), []byte(strconv.Itoa(n)+"\n"), 0o600)
}

// claimInstructions is what a woken idle session is told to do.
func claimInstructions(id string, n int) string {
	return "[agentmesh] " + strconv.Itoa(n) + " task(s) can be claimed by this session. If you are idle: call task_claim(session=" + id +
		"), do the task in this repo on a new branch, push the branch, call task_done with a summary (the requester is messaged), " +
		"then restart the waiter. If you are in the middle of other work, do not claim; just restart the waiter.\n"
}
