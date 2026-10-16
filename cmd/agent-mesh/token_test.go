package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felixn678/agent-mesh/internal/config"
)

func TestTokenNewAdminAndHost(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	adminFile := filepath.Join(dir, "admin.token")
	if err := runToken(&out, []string{"new", "--admin", "--out", adminFile}); err != nil {
		t.Fatal(err)
	}
	tok, err := config.ReadToken(adminFile)
	if err != nil {
		t.Fatal(err)
	}
	if want := `admin_token_hash = "` + config.HashToken(tok) + `"`; !strings.Contains(out.String(), want) {
		t.Fatalf("output %q lacks %q", out.String(), want)
	}
	if strings.Contains(out.String(), tok) {
		t.Fatal("raw token printed")
	}
	if st, _ := os.Stat(adminFile); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}

	out.Reset()
	if err := runToken(&out, []string{"new", "--host", "box", "--out", filepath.Join(dir, "box.token")}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), `box = "sha256:`) || !strings.Contains(out.String(), "[host_tokens]") {
		t.Fatal(out.String())
	}

	out.Reset()
	if err := runToken(&out, []string{"new", "--session", "box", "--out", filepath.Join(dir, "box-session.token")}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), `box = "sha256:`) || !strings.Contains(out.String(), "[session_tokens]") {
		t.Fatal(out.String())
	}
}

func TestTokenNewRejectsBadArgs(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "x")
	os.WriteFile(existing, []byte("t"), 0o600)
	bad := [][]string{
		{}, {"old"}, {"new", "--out", "f"}, {"new", "--admin", "--host", "a", "--out", "f"},
		{"new", "--host", "Bad Name", "--out", filepath.Join(dir, "y")},
		{"new", "--host", "admin", "--out", filepath.Join(dir, "y")},
		{"new", "--admin"},
		{"new", "--session", "a", "--host", "a", "--out", filepath.Join(dir, "z")},
		{"new", "--admin", "--out", existing},
	}
	for _, a := range bad {
		if err := runToken(&bytes.Buffer{}, a); err == nil {
			t.Errorf("accepted %v", a)
		}
	}
}
