package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenRoundTrip(t *testing.T) {
	tok, err := NewToken()
	if err != nil || len(tok) != 64 {
		t.Fatalf("%q %v", tok, err)
	}
	path := filepath.Join(t.TempDir(), "sub", "t.token")
	if err := WriteToken(path, tok); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	if err := WriteToken(path, "other"); err == nil {
		t.Fatal("overwrote existing token")
	}
	got, err := ReadToken(path)
	if err != nil || got != tok {
		t.Fatalf("%q %v", got, err)
	}
	h, err := ParseHash(HashToken(tok))
	if err != nil || len(h) != 32 {
		t.Fatal(err)
	}
}

func TestReadTokenRejectsOpenPermsAndEmpty(t *testing.T) {
	dir := t.TempDir()
	open := filepath.Join(dir, "open")
	os.WriteFile(open, []byte("abc\n"), 0o644)
	if _, err := ReadToken(open); err == nil {
		t.Fatal("accepted world-readable token")
	}
	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, []byte("\n"), 0o600)
	if _, err := ReadToken(empty); err == nil {
		t.Fatal("accepted empty token")
	}
}

func TestParseHashRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "abc", "sha256:zz", "sha256:abcd", "md5:" + HashToken("x")[7:]} {
		if _, err := ParseHash(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestLoadTOMLStrictAndDuration(t *testing.T) {
	type cfg struct {
		TTL time.Duration `toml:"ttl"`
	}
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte(`ttl = "5m"`), 0o600)
	var c cfg
	if err := LoadTOML(p, &c); err != nil || c.TTL != 5*time.Minute {
		t.Fatalf("%+v %v", c, err)
	}
	os.WriteFile(p, []byte("ttl = \"5m\"\ntypo = 1"), 0o600)
	if err := LoadTOML(p, &c); err == nil {
		t.Fatal("unknown key accepted")
	}
}

func TestExpandHome(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	if got, _ := ExpandHome("~/x/y"); got != "/home/someone/x/y" {
		t.Fatal(got)
	}
	if got, _ := ExpandHome("/abs"); got != "/abs" {
		t.Fatal(got)
	}
}
