package daemon

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// workerStrippedEnv never reaches claude: GitHub credentials are for the
// daemon's own git/gh calls only, and hub tokens stay with the daemon.
var workerStrippedEnv = []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "AGENTMESH_TOKEN"}

// readEnvFile parses KEY=VALUE lines ("#" comments, blank lines ignored).
func readEnvFile(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	if err := checkPrivateFile(path); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		if !ok || k == "" {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, n)
		}
		out = append(out, k+"="+strings.Trim(strings.TrimSpace(v), `"'`))
	}
	return out, sc.Err()
}

// buildEnvs returns the claude env (os env minus credentials, plus the claude
// env file, plus AGENTMESH_HOOKS=off so user-scope agentmesh hooks no-op) and
// the git/gh env (os env plus the gh env file, no terminal prompts).
func buildEnvs(base []string, claudeFile, ghFile string) (claudeEnv, gitEnv []string, err error) {
	cf, err := readEnvFile(claudeFile)
	if err != nil {
		return nil, nil, err
	}
	gf, err := readEnvFile(ghFile)
	if err != nil {
		return nil, nil, err
	}
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !contains(workerStrippedEnv, k) && k != "AGENTMESH_HOOKS" {
			claudeEnv = append(claudeEnv, kv)
		}
	}
	claudeEnv = append(append(claudeEnv, cf...), "AGENTMESH_HOOKS=off")
	gitEnv = append(append(append([]string{}, base...), gf...), "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1")
	return claudeEnv, gitEnv, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// cmdError carries the exit code and a redacted stderr tail of a failed call.
type cmdError struct {
	Name   string
	Code   int
	Stderr string
	Err    error
}

func (e *cmdError) Error() string {
	return fmt.Sprintf("%s: %v: %s", e.Name, e.Err, short(e.Stderr))
}

func (e *cmdError) Unwrap() error { return e.Err }

// runCmd runs one external command (never through a shell) bounded by
// timeout and returns its stdout.
func runCmd(ctx context.Context, timeout time.Duration, dir string, env []string, stdin io.Reader,
	name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env, cmd.Stdin = dir, env, stdin
	cmd.WaitDelay = 5 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		sub := name
		for i := 0; i < len(args); i++ {
			if args[i] == "-c" {
				i++
				continue
			}
			sub += " " + args[i]
			break
		}
		return out.String(), &cmdError{Name: sub, Code: code, Stderr: errb.String(), Err: err}
	}
	return out.String(), nil
}

// exitCode extracts the exit code from a runCmd error (-1 if unknown).
func exitCode(err error) int {
	if ce, ok := err.(*cmdError); ok {
		return ce.Code
	}
	return -1
}
