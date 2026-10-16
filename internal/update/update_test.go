package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func run(o *Options, args ...string) error { return Run(context.Background(), o, args) }

func mustContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s: content mismatch (err %v, %d bytes)", path, err, len(got))
	}
}

func TestUpdateReplacesBinaryAndRestartsHubThenDaemon(t *testing.T) {
	base, _ := serve(t, releaseDir(t, "v1.2.0"))
	exe := installed(t)
	o, out, svc := options(t, "v1.1.0", base, exe)
	if err := run(o); err != nil {
		t.Fatalf("update: %v\n%s", err, out)
	}
	mustContent(t, exe, releaseBinary(t, "v1.2.0"))
	if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v, want 0755", fi.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".agent-mesh.new.*")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	if !slices.Equal(svc.restarted, []string{"hub", "daemon"}) {
		t.Fatalf("restarts %v, want hub then daemon", svc.restarted)
	}
	for _, want := range []string{"v1.1.0 -> v1.2.0", "update the hub machine first"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestUpdateNoRestartAndRestartFailure(t *testing.T) {
	base, _ := serve(t, releaseDir(t, "v1.2.0"))
	o, out, svc := options(t, "v1.1.0", base, installed(t))
	if err := run(o, "--no-restart"); err != nil {
		t.Fatal(err)
	}
	if len(svc.restarted) != 0 || !strings.Contains(out.String(), "--no-restart") {
		t.Fatalf("restarted %v despite --no-restart:\n%s", svc.restarted, out)
	}

	o, _, svc = options(t, "v1.1.0", base, installed(t))
	svc.fail, svc.other = "hub", []string{"daemon"}
	svc.running = []string{"hub"}
	if err := run(o); err == nil || !strings.Contains(err.Error(), "restart hub service") {
		t.Fatalf("want restart error, got %v", err)
	}
}

func TestUpdateRefusesBadDownloads(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		spoil      func(dir string)
	}{
		{"tampered archive", "checksum mismatch", func(dir string) {
			p := filepath.Join(dir, fmt.Sprintf("agent-mesh-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH))
			b, _ := os.ReadFile(p)
			b[len(b)/2] ^= 0xff
			os.WriteFile(p, b, 0o644)
		}},
		{"missing asset", "404", func(dir string) {
			os.Remove(filepath.Join(dir, fmt.Sprintf("agent-mesh-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)))
		}},
		{"no checksum entry", "SHA256SUMS has no entry", func(dir string) {
			os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte("00  other.tar.gz\n"), 0o644)
		}},
		{"missing checksums", "SHA256SUMS", func(dir string) { os.Remove(filepath.Join(dir, "SHA256SUMS")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := releaseDir(t, "v1.2.0")
			tc.spoil(dir)
			base, _ := serve(t, dir)
			exe := installed(t)
			o, _, svc := options(t, "v1.1.0", base, exe)
			if err := run(o); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
			mustContent(t, exe, []byte("old binary"))
			if len(svc.restarted) != 0 {
				t.Fatal("services restarted after a refused update")
			}
		})
	}
}

func TestCheckExitCodes(t *testing.T) {
	base, _ := serve(t, releaseDir(t, "v1.2.0"))
	for _, tc := range []struct {
		current, want string
		code          int
	}{
		{"v1.1.0", "update available", ExitUpdateAvailable},
		{"v1.2.0", "already up to date", 0},
		{"v1.3.0", "newer than the release", 0},
	} {
		exe := installed(t)
		o, out, svc := options(t, tc.current, base, exe)
		err := run(o, "--check")
		var exit ExitError
		if code := 0; err != nil {
			if !errors.As(err, &exit) {
				t.Fatalf("%s: %v", tc.current, err)
			}
			code = exit.Code
			if code != tc.code {
				t.Fatalf("%s: exit %d, want %d", tc.current, code, tc.code)
			}
		} else if tc.code != 0 {
			t.Fatalf("%s: exit 0, want %d", tc.current, tc.code)
		}
		if !strings.Contains(out.String(), tc.want) || !strings.Contains(out.String(), "latest:  v1.2.0") {
			t.Fatalf("%s: output:\n%s", tc.current, out)
		}
		mustContent(t, exe, []byte("old binary"))
		if len(svc.restarted) != 0 {
			t.Fatal("--check restarted services")
		}
	}
}

func TestSameVersionAndDowngradeNeedsExplicitVersion(t *testing.T) {
	base, _ := serve(t, releaseDir(t, "v1.1.0"))
	for _, current := range []string{"v1.1.0", "v1.2.0"} {
		exe := installed(t)
		o, out, svc := options(t, current, base, exe)
		if err := run(o); err != nil {
			t.Fatal(err)
		}
		mustContent(t, exe, []byte("old binary"))
		if len(svc.restarted) != 0 || out.Len() == 0 {
			t.Fatalf("%s: unexpected change:\n%s", current, out)
		}
	}

	exe := installed(t)
	o, out, _ := options(t, "v1.2.0", base, exe)
	if err := run(o, "--version", "1.1.0"); err != nil {
		t.Fatalf("rollback: %v\n%s", err, out)
	}
	mustContent(t, exe, releaseBinary(t, "v1.1.0"))

	o, _, _ = options(t, "v1.2.0", base, installed(t))
	if err := run(o, "--version", "v1.0.0"); err == nil || !strings.Contains(err.Error(), "contains agent-mesh v1.1.0") {
		t.Fatalf("want tag mismatch refusal, got %v", err)
	}
}

func TestDevelopmentBuilds(t *testing.T) {
	base, hits := serve(t, releaseDir(t, "v1.2.0"))
	for _, dev := range []string{"dev", "v0.0.0-20261016120000-abcdef123456", "v1.2.1-0.20261016120000-abcdef123456+dirty"} {
		o, out, _ := options(t, dev, base, installed(t))
		if err := run(o, "--check"); err != nil || !strings.Contains(out.String(), "development build; use a release") {
			t.Fatalf("%s --check: %v\n%s", dev, err, out)
		}
		if err := run(o); err == nil || !strings.Contains(err.Error(), "--version") {
			t.Fatalf("%s: update without --version must be refused, got %v", dev, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("development builds downloaded %d files without --version", hits.Load())
	}
	exe := installed(t)
	o, _, _ := options(t, "dev", base, exe)
	if err := run(o, "--version", "v1.2.0"); err != nil {
		t.Fatal(err)
	}
	mustContent(t, exe, releaseBinary(t, "v1.2.0"))
}

func TestGoManagedAndUnwritableBinaries(t *testing.T) {
	base, hits := serve(t, releaseDir(t, "v1.2.0"))
	o, _, _ := options(t, "v1.1.0", base, "")
	o.Exe = filepath.Join(o.GOPATH, "bin", "agent-mesh")
	err := run(o, "--version", "v1.2.0")
	if err == nil || !strings.Contains(err.Error(), "go install github.com/felixn678/agent-mesh/cmd/agent-mesh@v1.2.0") {
		t.Fatalf("want go install hint, got %v", err)
	}
	o.Exe = "/tmp/go-build123/b001/exe/agent-mesh"
	if err := run(o); err == nil || !strings.Contains(err.Error(), "@latest") {
		t.Fatalf("want go install hint for go run binary, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("downloaded for a Go-managed binary")
	}

	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	exe := installed(t)
	os.Chmod(filepath.Dir(exe), 0o555)
	t.Cleanup(func() { os.Chmod(filepath.Dir(exe), 0o755) })
	o, _, svc := options(t, "v1.1.0", base, exe)
	err = run(o)
	if err == nil || !strings.Contains(err.Error(), "AGENTMESH_VERSION=v1.2.0 sh") {
		t.Fatalf("want installer hint, got %v", err)
	}
	mustContent(t, exe, []byte("old binary"))
	if len(svc.restarted) != 0 {
		t.Fatal("restarted after a failed replace")
	}
}

func TestDownloadSchemesAndPlatform(t *testing.T) {
	dir := releaseDir(t, "v1.2.0")
	exe := installed(t)
	o, _, _ := options(t, "v1.1.0", "file://"+dir, exe)
	if err := run(o, "--no-restart"); err != nil {
		t.Fatalf("file:// base: %v", err)
	}
	mustContent(t, exe, releaseBinary(t, "v1.2.0"))

	o.BaseURL = ""
	if _, err := o.fetch(context.Background(), "http://example.invalid", "SHA256SUMS", 10); err == nil || !strings.Contains(err.Error(), "only https") {
		t.Fatalf("plain http to GitHub must be refused: %v", err)
	}

	// An archive named for another CPU but holding this machine's binary.
	o.BaseURL, o.GOARCH = "file://"+dir, "riscv64"
	other := filepath.Join(dir, fmt.Sprintf("agent-mesh-%s-riscv64.tar.gz", runtime.GOOS))
	b, _ := os.ReadFile(filepath.Join(dir, fmt.Sprintf("agent-mesh-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)))
	writeFile(t, other, b)
	sums, _ := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	writeFile(t, filepath.Join(dir, "SHA256SUMS"), append(sums, strings.Replace(string(sums), runtime.GOARCH, "riscv64", 1)...))
	if err := run(o, "--version", "v1.2.0"); err == nil || !strings.Contains(err.Error(), "GOARCH") {
		t.Fatalf("want platform refusal, got %v", err)
	}
}

func TestRejectsNonReleaseVersionFlag(t *testing.T) {
	o, _, _ := options(t, "v1.1.0", "file:///nonexistent", installed(t))
	for _, v := range []string{"latest", "v1.2", "../v1.2.3", "v1.2.3/x"} {
		if err := run(o, "--version", v); err == nil || !strings.Contains(err.Error(), "not a release tag") {
			t.Fatalf("--version %q: %v", v, err)
		}
	}
}
