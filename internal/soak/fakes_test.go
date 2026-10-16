//go:build soak

package soak

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain doubles the test binary as fake claude, gh and gitleaks for the
// real daemons: tests symlink it under those names (role from argv[0]).
func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "claude":
		os.Exit(fakeClaude())
	case "gh":
		os.Exit(fakeGH())
	case "gitleaks":
		if len(os.Args) > 1 && os.Args[1] == "stdin" {
			_, _ = io.Copy(io.Discard, os.Stdin)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeClaude "works" 1-3 s, commits one file and prints a result line. It
// dies on SIGTERM (default Go behaviour), like the real CLI.
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
	_, _ = io.ReadAll(os.Stdin)
	sid := fmt.Sprintf("soak-%d", os.Getpid())
	fmt.Printf(`{"type":"system","subtype":"init","session_id":%q}`+"\n", sid)
	time.Sleep(time.Duration(1000+rand.IntN(2000)) * time.Millisecond)
	name := fmt.Sprintf("work-%d.txt", os.Getpid())
	if err := os.WriteFile(name, []byte("soak\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	for _, c := range [][]string{{"add", "--", name}, {"commit", "-q", "-m", "soak: " + name}} {
		if out, err := exec.Command("git", c...).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "git %v: %v %s\n", c, err, out)
			return 3
		}
	}
	fmt.Printf(`{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":%q,"total_cost_usd":0.01}`+"\n", sid)
	return 0
}

type fakePR struct {
	Number int    `json:"number"`
	Head   string `json:"head"`
	State  string `json:"state"`
	URL    string `json:"url"`
}

type ghState struct {
	Next int      `json:"next"`
	PRs  []fakePR `json:"prs"`
}

// withGHState runs fn on the shared fake-gh state under an exclusive lock.
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
			_, _ = io.ReadAll(os.Stdin)
			s.Next++
			url := fmt.Sprintf("https://github.com/example/soak/pull/%d", s.Next)
			s.PRs = append(s.PRs, fakePR{s.Next, flag("--head"), "open", url})
			fmt.Println(url)
		case "pr close":
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
