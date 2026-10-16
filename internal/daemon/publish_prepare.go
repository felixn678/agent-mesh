package daemon

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Publish notes recorded on complete.
const (
	noteNoChanges = "no_changes"
	noteLeftover  = "leftover_commit"
	notePRFailed  = "pr_failed"
	// noteLeaseLostBeforePR: pushed, then the lease check failed, so no PR.
	noteLeaseLostBeforePR = "lease_lost_before_pr"
)

// gitleaksHit is the exit code we ask gitleaks to use for findings, so scan
// errors (exit 1) are not mistaken for clean or for hits.
const gitleaksHit = 42

// sensitive reports whether a changed path could alter agent or CI
// behavior for the next human or worker, or weaken the secret scan.
func sensitive(p string) bool {
	base := path.Base(p)
	switch {
	case strings.HasPrefix(p, ".claude/"), strings.Contains(p, "/.claude/"),
		strings.HasPrefix(p, ".github/workflows/"),
		p == ".mcp.json", base == "CLAUDE.md", base == "CLAUDE.local.md",
		base == ".gitleaks.toml", base == ".gitleaksignore":
		return true
	}
	return false
}

// prepare is publish s0. It returns a refusal reason (task goes stuck, the
// worktree is kept for a human), or the publish note, or an error for
// infrastructure failures.
func (d *Daemon) prepare(ctx context.Context, j *journal) (refuse, note string, err error) {
	wt, baseRef := j.Worktree, "refs/remotes/origin/"+j.Base
	status, err := d.git(ctx, wt, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", "", err
	}
	var untracked []string
	dirty := false
	for _, line := range strings.Split(strings.TrimRight(status, "\n"), "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, "?? "):
			untracked = append(untracked, strings.TrimPrefix(line, "?? "))
		default:
			dirty = true
		}
	}
	if len(untracked) > 0 {
		return fmt.Sprintf("untracked files left: %s", strings.Join(untracked[:min(5, len(untracked))], ", ")), "", nil
	}
	if dirty {
		msg := fmt.Sprintf("agent-mesh: commit leftover changes of task #%d", j.ID)
		if _, err := d.git(ctx, wt, "commit", "--quiet", "--no-verify", "-a", "-m", msg); err != nil {
			return "", "", err
		}
		note = noteLeftover
	}
	ahead, err := d.git(ctx, wt, "rev-list", "--count", "--end-of-options", baseRef+"..HEAD")
	if err != nil {
		return "", "", err
	}
	if n, _ := strconv.Atoi(strings.TrimSpace(ahead)); n == 0 {
		return "", noteNoChanges, nil
	}
	changed, err := d.git(ctx, wt, "diff", "--name-only", "--no-renames", "--end-of-options", baseRef, "HEAD")
	if err != nil {
		return "", "", err
	}
	for _, p := range strings.Fields(changed) {
		if sensitive(p) {
			return "sensitive path: " + p, "", nil
		}
	}
	_, err = runCmd(ctx, d.cfg.GitCallTimeout, wt, d.gitEnv, nil, d.cfg.GitleaksBin, "git",
		"--no-banner", "--redact", "--ignore-gitleaks-allow", "--log-level", "error",
		"--exit-code", strconv.Itoa(gitleaksHit), "--log-opts="+baseRef+"..HEAD", wt)
	switch {
	case exitCode(err) == gitleaksHit:
		return "secret scan hit", "", nil
	case err != nil:
		return "secret scan error: " + err.Error(), "", nil
	}
	return "", note, nil
}

// commitSubjects lists up to 20 commit subjects for the PR body.
func (d *Daemon) commitSubjects(ctx context.Context, j *journal) []string {
	out, err := d.git(ctx, j.Worktree, "log", "--format=%s", "-n", "20", "--end-of-options",
		"refs/remotes/origin/"+j.Base+"..HEAD")
	if err != nil {
		return nil
	}
	var subs []string
	for _, s := range strings.Split(strings.TrimSpace(out), "\n") {
		s = strings.Map(func(r rune) rune {
			if r < 0x20 || r == '`' || r == '@' {
				return ' '
			}
			return r
		}, s)
		if len(s) > 100 {
			s = s[:100]
		}
		if s != "" {
			subs = append(subs, s)
		}
	}
	return subs
}
