package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultDirOverride(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "identity-b")
	t.Setenv(ConfigDirEnv, dir+"/")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // must not matter
	got, err := DefaultDir()
	if err != nil || got != dir {
		t.Fatalf("DefaultDir() = %q, %v; want %q", got, err, dir)
	}
	t.Setenv(ConfigDirEnv, "relative/dir")
	if _, err := DefaultDir(); err == nil {
		t.Fatal("relative override accepted")
	}
}

func TestDefaultDirFallsBackToUserConfigDir(t *testing.T) {
	t.Setenv(ConfigDirEnv, "")
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skip("no user config dir:", err)
	}
	got, err := DefaultDir()
	if err != nil || got != filepath.Join(base, "agent-mesh") {
		t.Fatalf("DefaultDir() = %q, %v (GOOS %s)", got, err, runtime.GOOS)
	}
}
