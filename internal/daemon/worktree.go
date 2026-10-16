package daemon

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

// addWorktree fetches the base and creates the task worktree on a fresh
// branch, all under the repo lock.
func (d *Daemon) addWorktree(ctx context.Context, t api.ClaimedTask, n taskNames) error {
	lock := d.repoLock(t.Repo)
	lock.Lock()
	defer lock.Unlock()
	clone, err := d.ensureClone(ctx, t.Repo)
	if err != nil {
		return err
	}
	if err := d.fetchBase(ctx, clone, t.BaseBranch); err != nil {
		return err
	}
	_, err = d.git(ctx, clone, "worktree", "add", "--quiet", "-b", n.branch, "--end-of-options",
		n.worktree, "refs/remotes/origin/"+t.BaseBranch)
	return err
}

// removeWorktree deletes the worktree and its local branch (best effort).
func (d *Daemon) removeWorktree(ctx context.Context, repo, wt, branch string) {
	lock := d.repoLock(repo)
	lock.Lock()
	defer lock.Unlock()
	clone := d.clonePath(repo)
	if _, err := d.git(ctx, clone, "worktree", "remove", "--force", "--end-of-options", wt); err != nil {
		_ = os.RemoveAll(wt)
		_, _ = d.git(ctx, clone, "worktree", "prune")
	}
	if branch != "" {
		_, _ = d.git(ctx, clone, "branch", "-D", "--end-of-options", branch)
	}
}

// cloneOfWorktree reads the repo key from a worktree's .git file
// ("gitdir: <state>/repos/<repo>.git/worktrees/<name>").
func cloneOfWorktree(wt string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(wt, ".git"))
	if err != nil {
		return "", false
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir:"))
	clone := filepath.Dir(filepath.Dir(gitdir))
	esc, ok := strings.CutSuffix(filepath.Base(clone), ".git")
	if !ok {
		return "", false
	}
	repo, err := url.PathUnescape(esc)
	if err != nil {
		return "", false
	}
	_, err = api.CanonicalRepo(repo)
	return repo, err == nil
}

// gc removes stuck worktrees past stuck_retention, logs past log_retention,
// prunes worktree metadata and compacts each clone. Worktrees of running
// tasks and of pending journals are never touched.
func (d *Daemon) gc(ctx context.Context) {
	now := time.Now()
	ents, _ := os.ReadDir(d.dir("worktrees"))
	for _, e := range ents {
		wt := filepath.Join(d.dir("worktrees"), e.Name())
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < d.cfg.StuckRetention || d.busyWorktree(wt) {
			continue
		}
		if _, err := os.Stat(filepath.Join(d.dir("spool"), e.Name()+".json")); err == nil {
			continue
		}
		if repo, ok := cloneOfWorktree(wt); ok {
			d.removeWorktree(ctx, repo, wt, "agentmesh/"+e.Name())
		} else {
			_ = os.RemoveAll(wt)
		}
		d.log.Info("gc: removed worktree", "worktree", e.Name())
	}
	logs, _ := os.ReadDir(d.dir("logs"))
	for _, e := range logs {
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > d.cfg.LogRetention {
			_ = os.Remove(filepath.Join(d.dir("logs"), e.Name()))
		}
	}
	for repo := range d.cfg.Repos {
		clone := d.clonePath(repo)
		if _, err := os.Stat(clone); err != nil {
			continue
		}
		lock := d.repoLock(repo)
		lock.Lock()
		_, _ = d.git(ctx, clone, "worktree", "prune")
		if _, err := d.gitT(ctx, 10*d.cfg.GitCallTimeout, clone, "gc", "--quiet"); err != nil {
			d.log.Warn("gc: git gc failed", "repo", repo, "err", err)
		}
		lock.Unlock()
	}
}

func (d *Daemon) busyWorktree(wt string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, w := range d.running {
		if w == wt {
			return true
		}
	}
	return false
}
