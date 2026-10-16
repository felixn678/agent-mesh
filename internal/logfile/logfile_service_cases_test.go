package logfile

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A service manager may open the stderr file without O_APPEND. Truncating it
// must also rewind the descriptor, or the next write lands at the old offset
// and leaves a sparse file as large as before.
func TestTrimStderrRewindsNonAppendDescriptor(t *testing.T) {
	p := filepath.Join(t.TempDir(), "daemon.stderr.log")
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 200), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	trimStderr(f, 100)
	if _, err := f.Write([]byte("boot\n")); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Size() != 5 {
		t.Fatalf("size after trim+write = %d, want 5", st.Size())
	}
}

// If the live log file disappears (a user cleaning ~/Library/Logs), rotation
// must start a new file instead of writing to the unlinked one forever.
func TestRotateRecreatesDeletedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.log")
	r, err := Open(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	line := []byte(strings.Repeat("a", 59) + "\n")
	if _, err := r.Write(line); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write(line); err != nil { // exceeds max: rotates
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(b, line) {
		t.Fatalf("after deleted-file rotation: %q %v", b, err)
	}
}

// stdout opened separately on the same path is rewound too.
func TestRewindIfSameFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.log")
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 200), 0o600); err != nil {
		t.Fatal(err)
	}
	errF, _ := os.OpenFile(p, os.O_WRONLY, 0)
	outF, _ := os.OpenFile(p, os.O_WRONLY, 0)
	other, _ := os.CreateTemp(t.TempDir(), "other")
	defer errF.Close()
	defer outF.Close()
	defer other.Close()
	_, _ = outF.Seek(0, io.SeekEnd)
	_, _ = other.Write([]byte("keep"))
	if !trimStderr(errF, 100) {
		t.Fatal("not trimmed")
	}
	rewindIfSameFile(outF, errF)
	rewindIfSameFile(other, errF)
	if off, _ := outF.Seek(0, io.SeekCurrent); off != 0 {
		t.Errorf("stdout offset %d, want 0", off)
	}
	if off, _ := other.Seek(0, io.SeekCurrent); off != 4 {
		t.Errorf("unrelated file rewound to %d", off)
	}
}
