package logfile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingCapsSizeAndKeepsN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "daemon.log")
	r, err := Open(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 39) + "\n") // 40 bytes
	for range 20 {
		if _, err := r.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + ".1", path + ".2"} {
		st, err := os.Stat(p)
		if err != nil || st.Size() > 100 || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", p, st, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("more than 2 rotated files kept: %v", err)
	}
	if st, _ := os.Stat(filepath.Dir(path)); st.Mode().Perm() != 0o700 {
		t.Fatalf("log dir mode %v", st.Mode().Perm())
	}
}

func TestReopenAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.log")
	for _, s := range []string{"one\n", "two\n"} {
		r, err := Open(path, 1<<20, 3)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = r.Write([]byte(s))
		_ = r.Close()
	}
	if b, _ := os.ReadFile(path); string(b) != "one\ntwo\n" {
		t.Fatalf("got %q", b)
	}
}

func TestTrimStderrOnlyWhenLarge(t *testing.T) {
	dir := t.TempDir()
	small, big := filepath.Join(dir, "small"), filepath.Join(dir, "big")
	_ = os.WriteFile(small, []byte("boot error\n"), 0o600)
	_ = os.WriteFile(big, bytes.Repeat([]byte("x"), 200), 0o600)
	for _, p := range []string{small, big} {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		trimStderr(f, 100)
		_ = f.Close()
	}
	if st, _ := os.Stat(small); st.Size() == 0 {
		t.Error("small stderr file truncated")
	}
	if st, _ := os.Stat(big); st.Size() != 0 {
		t.Error("large stderr file kept")
	}
}
