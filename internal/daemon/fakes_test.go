package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain doubles the test binary as fake claude, gh and gitleaks: tests
// symlink it under those names and the role is picked from argv[0].
func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "claude":
		os.Exit(fakeClaude())
	case "gh":
		os.Exit(fakeGH())
	case "gitleaks":
		os.Exit(fakeGitleaks())
	}
	os.Exit(m.Run())
}

const fakeSessionID = "00000000-0000-4000-8000-0000000000aa"

// fakeResultText must never appear in a PR body.
const fakeResultText = "MODEL-TEXT-must-not-reach-the-PR"

// fakeClaude behaves per the first word of the prompt (stdin).
func fakeClaude() int {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--version" {
		fmt.Println("2.1.301 (Claude Code)")
		return 0
	}
	if len(args) > 1 && args[0] == "auth" {
		fmt.Println(`{"loggedIn": true}`)
		return 0
	}
	dir := os.Getenv("FAKE_DIR")
	_ = os.WriteFile(filepath.Join(dir, "claude.argv"), []byte(strings.Join(args, "\n")), 0o600)
	envSeen := fmt.Sprintf("GH_TOKEN=%s\nAGENTMESH_HOOKS=%s\n", os.Getenv("GH_TOKEN"), os.Getenv("AGENTMESH_HOOKS"))
	_ = os.WriteFile(filepath.Join(dir, "claude.env"), []byte(envSeen), 0o600)
	prompt, _ := io.ReadAll(os.Stdin)
	mode, _, _ := strings.Cut(strings.TrimSpace(string(prompt)), " ")
	fmt.Printf(`{"type":"system","subtype":"init","session_id":%q}`+"\n", fakeSessionID)
	write := func(name, body string) { _ = os.WriteFile(name, []byte(body), 0o644) }
	commit := func(files ...string) {
		for _, c := range [][]string{append([]string{"add", "--"}, files...), {"commit", "-q", "-m", "fake: " + mode}} {
			if out, err := exec.Command("git", c...).CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "git %v: %v %s\n", c, err, out)
				os.Exit(3)
			}
		}
	}
	isErr, subtype := false, "success"
	switch mode {
	case "commit":
		write("agent.txt", string(prompt))
		commit("agent.txt")
	case "slowcommit":
		time.Sleep(1500 * time.Millisecond)
		write("agent.txt", string(prompt))
		commit("agent.txt")
	case "sleep":
		_ = os.WriteFile(filepath.Join(dir, "claude.pid"), []byte(fmt.Sprint(os.Getpid())), 0o600)
		time.Sleep(60 * time.Second)
	case "noresult":
		fmt.Fprintln(os.Stderr, "boom: upstream connection reset")
		return 1
	case "error":
		isErr, subtype = true, "error_max_budget_usd"
	case "untracked":
		write("stray.txt", "left behind")
	case "dirty":
		write("README.md", "changed but not committed\n")
	case "sensitive":
		write(".mcp.json", `{"mcpServers":{}}`)
		commit(".mcp.json")
	case "secret":
		write("config.txt", "token = \"ghp_"+strings.Repeat("aB3dE5fG7h", 3)+"abcdef\"\n")
		commit("config.txt")
	case "sensitivequoted": // git quotes non-ASCII paths unless -z is used
		_ = os.MkdirAll("ünïcode", 0o755)
		write("ünïcode/CLAUDE.md", "# rules for the next agent\n")
		commit("ünïcode/CLAUDE.md")
	case "symlinkclaude": // .claude is a symlink entry, not a directory prefix
		_ = os.MkdirAll("notes", 0o755)
		write("notes/settings.json", `{"hooks":{}}`)
		_ = os.Symlink("notes", ".claude")
		commit("notes/settings.json", ".claude")
	case "redirectgit":
		redirectGitDir(dir)
	case "nochange":
	}
	fmt.Printf(`{"type":"result","subtype":%q,"is_error":%v,"result":%q,"session_id":%q,"total_cost_usd":0.25}`+"\n",
		subtype, isErr, fakeResultText, fakeSessionID)
	return 0
}

type fakePR struct {
	Number int    `json:"number"`
	Head   string `json:"head"`
	Base   string `json:"base"`
	State  string `json:"state"`
	URL    string `json:"url"`
	Body   string `json:"body"`
}

type ghState struct {
	Next  int      `json:"next"`
	PRs   []fakePR `json:"prs"`
	Calls []string `json:"calls"`
}

// withGHState runs fn on the fake gh state under an exclusive file lock.
func withGHState(dir string, fn func(s *ghState)) {
	lf, err := os.OpenFile(filepath.Join(dir, "gh.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		panic(err)
	}
	defer lf.Close()
	_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_EX)
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	path := filepath.Join(dir, "gh.json")
	var s ghState
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	fn(&s)
	b, _ := json.Marshal(s)
	_ = os.WriteFile(path, b, 0o600)
}

func fakeGH() int {
	dir, args := os.Getenv("FAKE_DIR"), os.Args[1:]
	if len(args) >= 2 && args[0] == "auth" {
		if args[1] == "token" {
			fmt.Println("fake-token")
		}
		return 0
	}
	flag := func(name string) string {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == name {
				return args[i+1]
			}
		}
		return ""
	}
	code := 0
	withGHState(dir, func(s *ghState) {
		s.Calls = append(s.Calls, strings.Join(args[:min(2, len(args))], " "))
		switch strings.Join(args[:min(2, len(args))], " ") {
		case "pr list":
			type row struct {
				Number int    `json:"number"`
				Head   string `json:"headRefName"`
				URL    string `json:"url"`
			}
			out := []row{}
			for _, p := range s.PRs {
				if p.State == "open" {
					out = append(out, row{p.Number, p.Head, p.URL})
				}
			}
			b, _ := json.Marshal(out)
			fmt.Println(string(b))
		case "pr create":
			body, _ := io.ReadAll(os.Stdin)
			s.Next++
			url := fmt.Sprintf("https://github.com/example/demo/pull/%d", s.Next)
			s.PRs = append(s.PRs, fakePR{s.Next, flag("--head"), flag("--base"), "open", url, string(body)})
			fmt.Println(url)
		case "pr close":
			if err := os.Remove(filepath.Join(dir, "gh-close-fail")); err == nil {
				fmt.Fprintln(os.Stderr, "HTTP 502")
				code = 1
				return
			}
			for i := range s.PRs {
				if s.PRs[i].URL == args[2] {
					s.PRs[i].State = "closed"
				}
			}
		default:
			code = 2
		}
	})
	return code
}

// fakeGitleaks is used only when the real gitleaks is not installed.
func fakeGitleaks() int {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("fake")
	}
	if len(os.Args) > 1 && os.Args[1] == "stdin" {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	return 0
}

// redirectGitDir plays a hostile worker: it points the worktree's .git file
// at a git dir it controls whose config runs a program (core.fsmonitor) on
// the next git status. The program records the environment it ran with.
func redirectGitDir(fakeDir string) {
	wt, _ := os.Getwd()
	hook := filepath.Join(fakeDir, "fsmonitor.sh")
	_ = os.WriteFile(hook, []byte("#!/bin/sh\nenv > "+filepath.Join(fakeDir, "pwned")+"\nexit 1\n"), 0o755)
	evil := filepath.Join(wt, ".evil")
	for _, c := range [][]string{
		{"init", "-q", "--bare", evil},
		{"--git-dir=" + evil, "config", "core.bare", "false"},
		{"--git-dir=" + evil, "config", "core.worktree", wt},
		{"--git-dir=" + evil, "--work-tree=" + wt, "add", "README.md"},
		{"--git-dir=" + evil, "config", "core.fsmonitor", hook},
	} {
		if out, err := exec.Command("git", c...).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "git %v: %v %s\n", c, err, out)
			os.Exit(3)
		}
	}
	_ = os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+evil+"\n"), 0o644)
}
