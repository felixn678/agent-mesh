package e2e_test

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeRelease writes a release directory like the one scripts/build-release.sh
// produces, holding a stub agent-mesh for this OS/arch that prints version.
func fakeRelease(t *testing.T, version string) (dir, asset string) {
	t.Helper()
	dir = t.TempDir()
	asset = "agent-mesh-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
	f, err := os.Create(filepath.Join(dir, asset))
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	stub := []byte("#!/bin/sh\necho " + version + "\n")
	if err := tw.WriteHeader(&tar.Header{Name: "agent-mesh", Mode: 0o755, Size: int64(len(stub))}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write(stub)
	for _, c := range []interface{ Close() error }{tw, gz, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, asset))
	sum := sha256.Sum256(data)
	sums := hex.EncodeToString(sum[:]) + "  " + asset + "\n" + strings.Repeat("0", 64) + "  agent-mesh-other-os.tar.gz\n"
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, asset
}

// runInstaller runs scripts/install.sh with a POSIX shell (dash when present)
// in a fake HOME, never touching the network or the real home directory.
func runInstaller(t *testing.T, home string, env ...string) (string, error) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("installer supports Linux and macOS")
	}
	for _, tool := range []string{"curl", "tar", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not available")
		}
	}
	shell := "sh"
	if _, err := exec.LookPath("dash"); err == nil {
		shell = "dash"
	}
	script, err := filepath.Abs("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shell, script)
	cmd.Env = append([]string{"HOME=" + home, "PATH=/usr/bin:/bin:/usr/sbin:/sbin"}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstallScriptVerifiesAndInstalls(t *testing.T) {
	rel, _ := fakeRelease(t, "v1.2.3")
	home := t.TempDir()
	base := "AGENTMESH_DOWNLOAD_BASE=file://" + rel

	out, err := runInstaller(t, home, base) // default target: ~/.local/bin
	bin := filepath.Join(home, ".local", "bin", "agent-mesh")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	for _, want := range []string{"installed agent-mesh v1.2.3 to " + bin, "is not on your PATH",
		"agent-mesh up", "agent-mesh join", "agent-mesh invite <name>"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if fi, err := os.Stat(bin); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("installed binary: %v %v", fi, err)
	}

	// Reinstall over an existing binary into a dir already on PATH: no warning.
	rel2, _ := fakeRelease(t, "v1.2.4")
	dir := filepath.Dir(bin)
	out, err = runInstaller(t, home, "AGENTMESH_DOWNLOAD_BASE=file://"+rel2, "AGENTMESH_INSTALL_DIR="+dir,
		"PATH="+dir+":/usr/bin:/bin:/usr/sbin:/sbin")
	if err != nil || !strings.Contains(out, "installed agent-mesh v1.2.4") || strings.Contains(out, "not on your PATH") {
		t.Fatalf("reinstall: %v\n%s", err, out)
	}
}

func TestInstallScriptRefusesBadChecksum(t *testing.T) {
	rel, asset := fakeRelease(t, "v1.2.3")
	if err := os.WriteFile(filepath.Join(rel, "SHA256SUMS"), []byte(strings.Repeat("a", 64)+"  "+asset+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	out, err := runInstaller(t, home, "AGENTMESH_DOWNLOAD_BASE=file://"+rel)
	if err == nil || !strings.Contains(out, "checksum mismatch") {
		t.Fatalf("tampered release installed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "agent-mesh")); !os.IsNotExist(err) {
		t.Fatalf("binary written despite checksum mismatch: %v", err)
	}
}

func TestInstallScriptFailsOnMissingAsset(t *testing.T) {
	out, err := runInstaller(t, t.TempDir(), "AGENTMESH_DOWNLOAD_BASE=file://"+t.TempDir())
	if err == nil || !strings.Contains(out, "download failed") {
		t.Fatalf("missing asset: %v\n%s", err, out)
	}
}
