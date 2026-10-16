package report

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/config"
)

// defaultHubURL is the v1 loopback hub.
const defaultHubURL = "http://127.0.0.1:8787"

// Run is the `agent-mesh report` entry point (args exclude the subcommand).
func Run(ctx context.Context, args []string) error { return run(ctx, args, os.Stdout) }

func run(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	since := fs.Duration("since", 16*time.Hour, "report window ending now")
	out := fs.String("out", "", "write the report to this file (0600) instead of stdout; refused inside a git work tree")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 || *since <= 0 {
		return errors.New("usage: agent-mesh report [--since 16h] [--out path]")
	}
	url := os.Getenv("AGENTMESH_URL")
	if url == "" {
		url = defaultHubURL
	}
	tok, err := adminToken()
	if err != nil {
		return err
	}
	md, err := client.New(url, tok).Report(ctx, time.Now().Add(-*since))
	if err != nil {
		return err
	}
	if *out == "" {
		_, err = io.WriteString(stdout, md)
		return err
	}
	if err := writeOutside(ctx, *out, md); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "wrote %s\n", *out)
	return err
}

// adminToken reads $AGENTMESH_TOKEN, else <config-dir>/admin.token.
func adminToken() (string, error) {
	if tok := strings.TrimSpace(os.Getenv("AGENTMESH_TOKEN")); tok != "" {
		return tok, nil
	}
	dir, err := config.DefaultDir()
	if err != nil {
		return "", err
	}
	return config.ReadToken(filepath.Join(dir, "admin.token"))
}

// writeOutside writes the report to path with mode 0600 (parents 0700). The
// report holds worktree paths and session ids, so it refuses any target inside
// a git work tree where it could be committed to a public repo by accident.
func writeOutside(ctx context.Context, path, md string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(abs)
	inside, err := insideWorkTree(ctx, existingAncestor(dir))
	if err != nil {
		return err
	}
	if inside {
		return fmt.Errorf("refusing to write %s: it is inside a git work tree", path)
	}
	if fi, err := os.Lstat(abs); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write %s: it is a symlink", path)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create report dir: %w", err)
	}
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create report: %w", err)
	}
	if err := f.Chmod(0o600); err != nil { // an existing file keeps its old mode otherwise
		_ = f.Close()
		return fmt.Errorf("chmod report: %w", err)
	}
	if _, err := f.WriteString(md); err != nil {
		_ = f.Close()
		return fmt.Errorf("write report: %w", err)
	}
	return f.Close()
}

// existingAncestor returns dir or its nearest existing parent, so the git
// check also covers directories --out is about to create.
func existingAncestor(dir string) string {
	for {
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

// insideWorkTree asks git whether dir is inside a work tree. git exiting
// non-zero means "not a repository"; git missing is an error, because then the
// check cannot be made.
func insideWorkTree(ctx context.Context, dir string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	outb, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check git work tree (git required for --out): %w", err)
	}
	return strings.TrimSpace(string(outb)) == "true", nil
}
