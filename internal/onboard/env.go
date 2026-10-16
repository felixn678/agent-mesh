// Package onboard implements the zero-friction setup commands: up, invite,
// join and doctor. Everything that touches the outside world (processes,
// service managers, the home directory) goes through Env so tests can run in
// temp directories with fakes.
package onboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/config"
)

// Runner looks up and runs external programs (tailscale, claude, gh, ...).
type Runner interface {
	LookPath(name string) (string, error)
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner is the real Runner backed by os/exec.
type ExecRunner struct{}

// LookPath implements Runner.
func (ExecRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

// Output implements Runner; stderr is folded into the output.
func (ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Env is everything the commands depend on.
type Env struct {
	Home      string // user home directory
	ConfigDir string // e.g. ~/.config/agent-mesh (0700)
	DataDir   string // e.g. ~/.local/share/agent-mesh (0700)
	Binary    string // absolute path of the agent-mesh binary services run
	GOOS      string
	Out       io.Writer
	Runner    Runner
	Mgr       Manager
	Hostname  func() (string, error)
}

// NewEnv builds the production Env from the current user's directories.
func NewEnv(out io.Writer) (*Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate home: %w", err)
	}
	cfg, err := config.DefaultDir()
	if err != nil {
		return nil, err
	}
	bin, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate binary: %w", err)
	}
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		bin = r
	}
	e := &Env{Home: home, ConfigDir: cfg, DataDir: filepath.Join(home, ".local", "share", "agent-mesh"),
		Binary: bin, GOOS: runtime.GOOS, Out: out, Runner: ExecRunner{}, Hostname: os.Hostname}
	e.Mgr = NewManager(e)
	return e, nil
}

// Path joins name onto the config directory.
func (e *Env) Path(name string) string { return filepath.Join(e.ConfigDir, name) }

func (e *Env) printf(format string, a ...any) { fmt.Fprintf(e.Out, format, a...) }

// ShortHostName is the default host name: the first DNS label, lowercased and
// reduced to the hub's host-name alphabet.
func (e *Env) ShortHostName() (string, error) {
	h, err := e.Hostname()
	if err != nil {
		return "", fmt.Errorf("read hostname: %w (pass --name)", err)
	}
	h, _, _ = strings.Cut(strings.ToLower(h), ".")
	h = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, h)
	if len(h) > 32 {
		h = h[:32]
	}
	if err := checkHostName(h); err != nil {
		return "", fmt.Errorf("%w (pass --name)", err)
	}
	return h, nil
}

// checkHostName rejects names the hub would refuse.
func checkHostName(name string) error {
	if !api.ValidHostName(name) || name == "admin" {
		return fmt.Errorf("invalid host name %q: use 1-32 chars of a-z, 0-9, '-' (and not \"admin\")", name)
	}
	return nil
}

// Run dispatches `up`, `invite`, `join` and `doctor`; args[0] is the command.
func Run(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 {
		return errors.New("missing onboarding command")
	}
	switch args[0] {
	case "up":
		return Up(ctx, e, args[1:])
	case "invite":
		return Invite(ctx, e, args[1:])
	case "join":
		return Join(ctx, e, args[1:])
	case "doctor":
		return Doctor(ctx, e, args[1:])
	}
	return fmt.Errorf("unknown command %q", args[0])
}
