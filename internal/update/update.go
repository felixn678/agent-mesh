// Package update implements `agent-mesh update`: download a GitHub release
// (the same archives and SHA256SUMS the installer uses), verify it, replace
// the running binary atomically and restart this machine's services.
package update

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Repo is the GitHub repository releases come from.
const Repo = "felixn678/agent-mesh"

// ExitUpdateAvailable is the --check exit code when a newer release exists.
const ExitUpdateAvailable = 10

// ExitError asks the caller to exit with Code without printing anything.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// ExitCode returns the process exit code.
func (e ExitError) ExitCode() int { return e.Code }

// Options is everything Run depends on, so tests use temp dirs and fakes.
type Options struct {
	Current       string // running version
	Exe           string // running binary, symlinks resolved
	BaseURL       string // AGENTMESH_DOWNLOAD_BASE; "" = GitHub
	GOOS, GOARCH  string
	Home          string
	GOPATH, GOBIN string // from the environment, for go-install detection
	HTTP          *http.Client
	Services      Services // nil: nothing to restart
	Out           io.Writer
}

func (o *Options) asset() string { return "agent-mesh-" + o.GOOS + "-" + o.GOARCH + ".tar.gz" }

func (o *Options) printf(format string, a ...any) { fmt.Fprintf(o.Out, format, a...) }

// Run executes `agent-mesh update [--version vX.Y.Z] [--check] [--no-restart]`.
func Run(ctx context.Context, o *Options, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(o.Out)
	want := fs.String("version", "", "install this release tag (also downgrades: the rollback path)")
	check := fs.Bool("check", false, "only compare with the latest release: exit 0 if current, 10 if an update is available")
	noRestart := fs.Bool("no-restart", false, "do not restart the hub/daemon services after updating")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	tag := ""
	if *want != "" {
		if tag = normalizeTag(*want); tag == "" {
			return fmt.Errorf("--version %q is not a release tag like v1.2.3", *want)
		}
	}
	dev := isDevBuild(o.Current)
	switch {
	case dev && *check:
		o.printf("agent-mesh %s is a development build; use a release (agent-mesh update --version vX.Y.Z)\n", o.Current)
		return nil
	case dev && tag == "":
		return fmt.Errorf("agent-mesh %s is a development build; pick a release with --version vX.Y.Z", o.Current)
	case !*check && o.goManaged(o.Exe):
		return fmt.Errorf("%s is managed by the Go tool; update it with:\n  %s", o.Exe, goInstallHint(tag))
	}

	// Stage next to the binary (a directory that allows exec, unlike some
	// /tmp mounts); --check on a read-only install falls back to the temp dir.
	dir, err := os.MkdirTemp(filepath.Dir(o.Exe), ".agent-mesh-update-")
	if err != nil {
		dir, err = os.MkdirTemp("", "agent-mesh-update-")
	}
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	rel, err := o.download(ctx, tag, dir)
	if err != nil {
		return err
	}
	if tag != "" && rel.Version != tag {
		return fmt.Errorf("release %s contains agent-mesh %s; not installing", tag, rel.Version)
	}

	cmp := 1 // dev builds: any explicit release is a change
	if !dev {
		cmp = compareTags(rel.Version, o.Current)
	}
	if *check {
		o.printf("current: %s\nlatest:  %s\n", o.Current, rel.Version)
		switch {
		case cmp > 0:
			o.printf("update available: run agent-mesh update\n")
			return ExitError{ExitUpdateAvailable}
		case cmp < 0:
			o.printf("this build is newer than the release\n")
		default:
			o.printf("already up to date\n")
		}
		return nil
	}
	switch {
	case cmp == 0:
		o.printf("agent-mesh %s is already up to date\n", o.Current)
		return nil
	case cmp < 0 && tag == "":
		o.printf("agent-mesh %s is newer than the latest release %s; to go back to it run: agent-mesh update --version %s\n",
			o.Current, rel.Version, rel.Version)
		return nil
	}

	if err := replaceBinary(rel.Path, o.Exe); err != nil {
		if errors.Is(err, errNotWritable) {
			return fmt.Errorf("cannot replace %s (%v); install the release into ~/.local/bin instead:\n  %s", o.Exe, err, installerHint(rel.Version))
		}
		return fmt.Errorf("replace %s: %w", o.Exe, err)
	}
	o.printf("updated agent-mesh %s -> %s (%s)\n", o.Current, rel.Version, o.Exe)
	restartErr := o.restart(ctx, *noRestart)
	o.printf("update the hub machine first, then the others (agent-mesh update on each)\n")
	return restartErr
}

// restart restarts the installed services that run the updated binary, hub
// first. Hooks and the MCP server need nothing: they exec the binary anew.
func (o *Options) restart(ctx context.Context, skip bool) error {
	if o.Services == nil {
		return nil
	}
	running, other := o.Services.Installed(o.Exe)
	for _, name := range other {
		o.printf("warning: the %s service runs another agent-mesh binary than %s; not restarted\n", name, o.Exe)
	}
	if len(running) == 0 {
		return nil
	}
	if skip {
		o.printf("services not restarted (--no-restart); they keep running the old version until restarted: %v\n", running)
		return nil
	}
	var errs []error
	for _, name := range running {
		if err := o.Services.Restart(ctx, name); err != nil {
			errs = append(errs, fmt.Errorf("restart %s service: %w", name, err))
			continue
		}
		o.printf("restarted the %s service\n", name)
	}
	return errors.Join(errs...)
}
