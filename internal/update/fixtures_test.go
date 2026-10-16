package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

var (
	buildDir  string
	buildMu   sync.Mutex
	builtBins = map[string][]byte{}
)

func TestMain(m *testing.M) {
	var err error
	if buildDir, err = os.MkdirTemp("", "update-test-"); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(buildDir)
	os.Exit(code)
}

// releaseBinary builds a tiny Go program that, like a release build, prints
// the version stamped into it with -X for `version`.
func releaseBinary(t *testing.T, v string) []byte {
	t.Helper()
	buildMu.Lock()
	defer buildMu.Unlock()
	if b, ok := builtBins[v]; ok {
		return b
	}
	src := filepath.Join(buildDir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"go.mod":  "module fakeagentmesh\n\ngo 1.21\n",
		"main.go": "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nvar version = \"dev\"\n\nfunc main() {\n\tif len(os.Args) > 1 && os.Args[1] == \"version\" {\n\t\tfmt.Println(version)\n\t}\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(buildDir, "bin-"+v)
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X main.version="+v, "-o", out, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake release: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	builtBins[v] = b
	return b
}

func tarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// releaseDir writes a release directory like build-release.sh: the archive
// for this platform holding a binary stamped v, and SHA256SUMS.
func releaseDir(t *testing.T, v string) string {
	t.Helper()
	dir := t.TempDir()
	asset := fmt.Sprintf("agent-mesh-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := tarGz(t, "agent-mesh", releaseBinary(t, v))
	writeFile(t, filepath.Join(dir, asset), archive)
	writeFile(t, filepath.Join(dir, "SHA256SUMS"), fmt.Appendf(nil, "%x  %s\n", sha256.Sum256(archive), asset))
	return dir
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// serve serves dir over HTTP and counts requests.
func serve(t *testing.T, dir string) (string, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	fs := http.FileServer(http.Dir(dir))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fs.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits
}

// fakeServices records restarts.
type fakeServices struct {
	running, other []string
	restarted      []string
	fail           string
}

func (f *fakeServices) Installed(string) ([]string, []string) { return f.running, f.other }

func (f *fakeServices) Restart(_ context.Context, name string) error {
	f.restarted = append(f.restarted, name)
	if name == f.fail {
		return fmt.Errorf("unit %s failed", name)
	}
	return nil
}

// installed is a stand-in for the running binary: a file in its own temp dir.
func installed(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "agent-mesh")
	writeFile(t, exe, []byte("old binary"))
	return exe
}

func options(t *testing.T, current, base, exe string) (*Options, *bytes.Buffer, *fakeServices) {
	var out bytes.Buffer
	svc := &fakeServices{running: []string{"hub", "daemon"}}
	return &Options{
		Current: current, Exe: exe, BaseURL: base,
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Home: t.TempDir(), GOPATH: t.TempDir(),
		HTTP: http.DefaultClient, Services: svc, Out: &out,
	}, &out, svc
}
