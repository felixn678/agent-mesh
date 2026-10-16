package daemon

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// git runs one git call in dir with the git/gh env and git_call_timeout.
// Repository hooks are disabled for every daemon git call.
func (d *Daemon) git(ctx context.Context, dir string, args ...string) (string, error) {
	return d.gitT(ctx, d.cfg.GitCallTimeout, dir, args...)
}

func (d *Daemon) gitT(ctx context.Context, timeout time.Duration, dir string, args ...string) (string, error) {
	full := append([]string{"-c", "core.hooksPath=/dev/null"}, args...)
	return runCmd(ctx, timeout, dir, d.gitEnv, nil, "git", full...)
}

// repoLock serializes all ref-changing operations on one daemon clone.
func (d *Daemon) repoLock(repo string) *sync.Mutex {
	d.repoMu.Lock()
	defer d.repoMu.Unlock()
	m := d.repoLocks[repo]
	if m == nil {
		m = &sync.Mutex{}
		d.repoLocks[repo] = m
	}
	return m
}

// clonePath escapes '/' so URL-style repo keys map to one flat directory.
func (d *Daemon) clonePath(repo string) string {
	return filepath.Join(d.dir("repos"), url.PathEscape(repo)+".git")
}

// ensureClone creates the daemon-owned bare clone on first use. Caller holds
// the repo lock. The user's own working clones are never touched.
func (d *Daemon) ensureClone(ctx context.Context, repo string) (string, error) {
	path := d.clonePath(repo)
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err == nil {
		return path, nil
	}
	remote := d.cfg.Repos[repo]
	if remote == "" {
		return "", fmt.Errorf("repo %q not configured", repo)
	}
	// init + remote instead of clone --bare: a bare clone copies origin's
	// branches into refs/heads, where they would collide with task branches.
	// Fetches are per base branch (fetchBase).
	tmp := path + ".tmp"
	_ = os.RemoveAll(tmp)
	if _, err := d.git(ctx, "", "init", "--quiet", "--bare", "--end-of-options", tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	for _, kv := range [][2]string{
		{"remote.origin.url", remote},
		{"remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
		{"gc.auto", "0"},
		{"credential.helper", "!" + d.cfg.GHBin + " auth git-credential"},
	} {
		if _, err := d.git(ctx, tmp, "config", "--end-of-options", kv[0], kv[1]); err != nil {
			_ = os.RemoveAll(tmp)
			return "", err
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

// fetchBase updates refs/remotes/origin/<base>. Caller holds the repo lock.
func (d *Daemon) fetchBase(ctx context.Context, clone, base string) error {
	_, err := d.git(ctx, clone, "fetch", "--quiet", "--no-tags", "--end-of-options", "origin",
		"+refs/heads/"+base+":refs/remotes/origin/"+base)
	return err
}

// remoteBranchSHA returns the sha of refs/heads/<branch> on origin ("" if absent).
func (d *Daemon) remoteBranchSHA(ctx context.Context, dir, branch string) (string, error) {
	out, err := d.git(ctx, dir, "ls-remote", "--end-of-options", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	sha, _, _ := strings.Cut(strings.TrimSpace(out), "\t")
	return sha, nil
}
