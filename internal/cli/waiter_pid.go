package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Waiter bookkeeping under <state-dir>/waiters: <id>.pid is the singleton
// lock (flock held by the live waiter, content = its pid for messages);
// <id>.reminded marks that the Stop hook already asked for a restart after
// the waiter died. The kernel drops the lock when the waiter dies, so a stale
// file or a reused pid can never block a restart. Session ids are validated
// (api.ValidSessionID) before they reach these paths.

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

// waiterAlive reports whether a live waiter holds the session's lock.
func waiterAlive(dir, id string) bool {
	if dir == "" {
		return true // unknown state dir: never nag
	}
	f, err := os.Open(pidPath(dir, id))
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// acquireWaiter takes the session's waiter lock. If a live waiter holds it,
// it returns that waiter's pid and a nil release. The file is never removed
// (removing it would let a racing starter lock an orphaned inode).
func acquireWaiter(dir, id string) (release func(), holder int, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, 0, err
	}
	f, err := os.OpenFile(pidPath(dir, id), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, 0, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, readPID(pidPath(dir, id)), nil
		}
		return nil, 0, err
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	_ = os.Remove(remindedPath(dir, id))
	return func() {
		_ = f.Truncate(0)
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, 0, nil
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
