// Package logfile gives long-running commands (hub, daemon) a size-capped,
// rotating log file, for service managers that do not rotate stdout (launchd).
package logfile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// Defaults used by Setup: at most MaxBytes per file plus Keep rotated files.
const (
	MaxBytes = 10 << 20
	Keep     = 3
	// maxStderrBytes caps the file a service manager attaches to stderr; it
	// then only collects startup failures and crashes.
	maxStderrBytes = 1 << 20
)

// Rotating appends to path and, when a write would exceed max bytes, renames
// path to path.1 (path.1 to path.2, ...), keeping keep old files.
type Rotating struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

// Open creates the directory (0700) and opens path for appending (0600).
func Open(path string, max int64, keep int) (*Rotating, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("log dir: %w", err)
	}
	r := &Rotating{path: path, max: max, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Rotating) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("open log: %w", err)
	}
	r.f, r.size = f, st.Size()
	return nil
}

// Write implements io.Writer. A failed rotation keeps writing to the current
// file rather than losing log lines.
func (r *Rotating) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		r.rotate()
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *Rotating) rotate() {
	_ = os.Remove(r.path + "." + strconv.Itoa(r.keep))
	for i := r.keep - 1; i >= 1; i-- {
		_ = os.Rename(r.path+"."+strconv.Itoa(i), r.path+"."+strconv.Itoa(i+1))
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return
	}
	// ErrNotExist: the live file was deleted under us (e.g. someone cleaned
	// the log directory); fall through and start a fresh one.
	old := r.f
	if err := r.open(); err != nil {
		r.f = old // keep the renamed file open; better than dropping lines
		return
	}
	_ = old.Close()
}

// Close closes the current file.
func (r *Rotating) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// Setup sends the default slog logger to a rotating file at path and trims
// an oversized stderr file (what launchd's StandardErrorPath points at).
func Setup(path string) (io.Closer, error) {
	r, err := Open(path, MaxBytes, Keep)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(r, nil)))
	if trimStderr(os.Stderr, maxStderrBytes) {
		rewindIfSameFile(os.Stdout, os.Stderr) // launchd points both at one file
	}
	return r, nil
}

// trimStderr empties f when it is a regular file larger than max, and
// rewinds it: a descriptor opened without O_APPEND would otherwise keep
// writing at the old offset and leave a sparse file as large as before.
func trimStderr(f *os.File, max int64) bool {
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() <= max {
		return false
	}
	_ = f.Truncate(0)
	_, _ = f.Seek(0, io.SeekStart)
	return true
}

// rewindIfSameFile rewinds f when it refers to the same file as trimmed (for
// example stdout and stderr opened separately on one log path).
func rewindIfSameFile(f, trimmed *os.File) {
	a, err1 := f.Stat()
	b, err2 := trimmed.Stat()
	if err1 == nil && err2 == nil && os.SameFile(a, b) {
		_, _ = f.Seek(0, io.SeekStart)
	}
}
