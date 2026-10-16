package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Waiter bookkeeping under <state-dir>/waiters: <id>.pid holds the live
// waiter's pid (singleton per session); <id>.reminded marks that the Stop
// hook already asked for a restart after the waiter died. Session ids are
// validated (api.ValidSessionID) before they reach these paths.

func waitersDir() string {
	dir, err := stateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "waiters")
}

func pidPath(dir, id string) string      { return filepath.Join(dir, id+".pid") }
func remindedPath(dir, id string) string { return filepath.Join(dir, id+".reminded") }

func readPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// processAlive uses signal 0; EPERM still means the process exists.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// waiterAlive reports whether a live waiter holds the session's pid file.
func waiterAlive(dir, id string) bool {
	if dir == "" {
		return true // unknown state dir: never nag
	}
	pid := readPID(pidPath(dir, id))
	return pid > 0 && processAlive(pid)
}

// acquireWaiter claims the session's pid file. If a live waiter holds it,
// it returns that pid and a nil release. Stale files are replaced.
func acquireWaiter(dir, id string) (release func(), holder int, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, 0, err
	}
	path := pidPath(dir, id)
	for range 2 {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			mine := strconv.Itoa(os.Getpid())
			_, werr := f.WriteString(mine + "\n")
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				_ = os.Remove(path)
				return nil, 0, werr
			}
			_ = os.Remove(remindedPath(dir, id))
			return func() {
				if readPID(path) == os.Getpid() {
					_ = os.Remove(path)
				}
			}, 0, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, 0, err
		}
		if pid := readPID(path); pid > 0 && processAlive(pid) {
			return nil, pid, nil
		}
		_ = os.Remove(path) // stale: the waiter died without cleanup
	}
	return nil, 0, errors.New("could not claim waiter pid file")
}

// markReminded records that the restart reminder was shown for the current
// waiter death; it returns false if it was already shown.
func markReminded(dir, id string) bool {
	if dir == "" || os.MkdirAll(dir, 0o700) != nil {
		return false
	}
	f, err := os.OpenFile(remindedPath(dir, id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// shellQuote quotes s for a POSIX shell unless it is plainly safe.
func shellQuote(s string) string {
	safe := s != ""
	for _, r := range s {
		if !(r == '/' || r == '.' || r == '_' || r == '-' || r == '+' || r == ':' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
