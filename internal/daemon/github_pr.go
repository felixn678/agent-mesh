package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/felixn678/agent-mesh/internal/api"
)

type ghPR struct {
	Number            int    `json:"number"`
	HeadRefName       string `json:"headRefName"`
	URL               string `json:"url"`
	IsCrossRepository bool   `json:"isCrossRepository"`
}

// gh runs the gh CLI in the task worktree (repo context from its origin
// remote), with the worktree's git dir pinned like every daemon git call.
func (d *Daemon) gh(ctx context.Context, j *journal, stdin string, args ...string) (string, error) {
	env, err := d.worktreeEnv(j.Repo, j.Worktree)
	if err != nil {
		return "", err
	}
	if stdin == "" {
		return runCmd(ctx, d.cfg.GitCallTimeout, j.Worktree, env, nil, d.cfg.GHBin, args...)
	}
	return runCmd(ctx, d.cfg.GitCallTimeout, j.Worktree, env, strings.NewReader(stdin), d.cfg.GHBin, args...)
}

// openPRs lists open PRs whose head branch lives in this repository.
func (d *Daemon) openPRs(ctx context.Context, j *journal) ([]ghPR, error) {
	out, err := d.gh(ctx, j, "", "pr", "list", "--state", "open", "--json", "number,headRefName,url,isCrossRepository",
		"--limit", "200")
	if err != nil {
		return nil, err
	}
	var all []ghPR
	if err := json.Unmarshal([]byte(out), &all); err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	// Only PRs whose head branch is in this repository are ours: a fork may
	// use the same branch name, and must be neither adopted nor closed.
	var prs []ghPR
	for _, pr := range all {
		if !pr.IsCrossRepository {
			prs = append(prs, pr)
		}
	}
	return prs, nil
}

// supersede closes open PRs of older attempts of the same task (s4).
func (d *Daemon) supersede(ctx context.Context, j *journal) {
	prs, err := d.openPRs(ctx, j)
	if err != nil {
		d.log.Warn("supersede: list PRs failed", "task", j.ID, "err", err)
		return
	}
	re := regexp.MustCompile(fmt.Sprintf(`^agentmesh/task-%d-f(\d+)$`, j.ID))
	for _, pr := range prs {
		m := re.FindStringSubmatch(pr.HeadRefName)
		if m == nil {
			continue
		}
		if k, _ := strconv.ParseInt(m[1], 10, 64); k < j.Fence {
			if err := d.closePR(ctx, j, pr.URL, "Superseded by a newer attempt ("+j.Branch+")."); err != nil {
				d.log.Warn("supersede: close failed", "task", j.ID, "pr", pr.URL, "err", err)
			}
		}
	}
}

// findOrCreatePR reuses an open PR whose head is the branch (crash after
// create), else opens a draft PR with a templated body (s5).
func (d *Daemon) findOrCreatePR(ctx context.Context, j *journal) (string, error) {
	prs, err := d.openPRs(ctx, j)
	if err != nil {
		return "", err
	}
	for _, pr := range prs {
		if pr.HeadRefName == j.Branch {
			return pr.URL, nil
		}
	}
	body := prBody(j, d.commitSubjects(ctx, j))
	_, err = runCmd(ctx, d.cfg.GitCallTimeout, j.Worktree, d.gitEnv, strings.NewReader(body), d.cfg.GitleaksBin,
		"stdin", "--no-banner", "--redact", "--log-level", "error", "--exit-code", strconv.Itoa(gitleaksHit))
	if err != nil {
		return "", fmt.Errorf("PR body secret scan: %w", err)
	}
	out, err := d.gh(ctx, j, body, "pr", "create", "--draft", "--base", j.Base, "--head", j.Branch,
		"--title", fmt.Sprintf("agent-mesh task #%d", j.ID), "--body-file", "-")
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	url := strings.TrimSpace(lines[len(lines)-1])
	if url == "" || !api.ValidPRURL(url) {
		return "", fmt.Errorf("gh pr create: unexpected output %q", tail(url, 200))
	}
	return url, nil
}

func (d *Daemon) closePR(ctx context.Context, j *journal, url, comment string) error {
	_, err := d.gh(ctx, j, "", "pr", "close", url, "--comment", comment)
	return err
}

// prBody is a fixed template: ids, numbers and commit subjects only. The
// model's final message is never published.
func prBody(j *journal, subjects []string) string {
	var b strings.Builder
	b.WriteString("Automated draft PR from agent-mesh. Review before merging.\n\n")
	fmt.Fprintf(&b, "- Task: #%d (attempt fence %d)\n", j.ID, j.Fence)
	fmt.Fprintf(&b, "- Base: %s\n", j.Base)
	fmt.Fprintf(&b, "- Cost: $%.2f\n", j.CostUSD)
	if j.SessionID != "" {
		fmt.Fprintf(&b, "- Claude session: %s\n", j.SessionID)
	}
	if j.PublishNote != "" {
		fmt.Fprintf(&b, "- Note: %s\n", j.PublishNote)
	}
	if len(subjects) > 0 {
		b.WriteString("\nCommits:\n")
		for _, s := range subjects {
			b.WriteString("- " + s + "\n")
		}
	}
	return b.String()
}
