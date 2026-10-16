package update

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// latestServer answers HEAD .../latest/download/SHA256SUMS like GitHub with
// a redirect to location ("" = no redirect, 404 when notFound); everything
// else is served from a v1.2.0 release directory and counted as a download.
func latestServer(t *testing.T, location string, notFound bool) (string, *atomic.Int32) {
	t.Helper()
	var downloads atomic.Int32
	files := http.StripPrefix("/latest/download", http.FileServer(http.Dir(releaseDir(t, "v1.2.0"))))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case notFound:
			http.NotFound(w, r)
		case r.Method == http.MethodHead && location != "":
			http.Redirect(w, r, location, http.StatusFound)
		default:
			if r.Method == http.MethodGet {
				downloads.Add(1)
			}
			files.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/latest/download", &downloads
}

func TestCheckReadsLatestTagFromRedirect(t *testing.T) {
	base, downloads := latestServer(t, "/owner/repo/releases/download/v1.3.0/SHA256SUMS", false)
	for _, tc := range []struct {
		current, want string
		code          int
	}{
		{"v1.2.0", "update available", ExitUpdateAvailable},
		{"v1.3.0", "already up to date", 0},
		{"v1.4.0", "newer than the release", 0},
	} {
		o, out, _ := options(t, tc.current, base, installed(t))
		err := run(o, "--check")
		var exit ExitError
		if code := 0; errors.As(err, &exit) {
			code = exit.Code
			if code != tc.code {
				t.Fatalf("%s: exit %d, want %d", tc.current, code, tc.code)
			}
		} else if err != nil || tc.code != 0 {
			t.Fatalf("%s: err %v, want exit %d", tc.current, err, tc.code)
		}
		if !strings.Contains(out.String(), "latest:  v1.3.0") || !strings.Contains(out.String(), tc.want) {
			t.Fatalf("%s: output:\n%s", tc.current, out)
		}
	}
	if n := downloads.Load(); n != 0 {
		t.Fatalf("--check downloaded %d files despite the redirect", n)
	}
}

// Without a usable redirect (a mirror answering 200, or a Location that is
// not a release tag) --check downloads the release to read its version.
func TestCheckFallsBackToDownload(t *testing.T) {
	for _, location := range []string{"", "/owner/repo/releases/download/v1.3/SHA256SUMS", "/elsewhere"} {
		base, downloads := latestServer(t, location, false)
		o, out, _ := options(t, "v1.1.0", base, installed(t))
		var exit ExitError
		if err := run(o, "--check"); !errors.As(err, &exit) || exit.Code != ExitUpdateAvailable {
			t.Fatalf("location %q: %v\n%s", location, err, out)
		}
		if !strings.Contains(out.String(), "latest:  v1.2.0") || downloads.Load() != 2 {
			t.Fatalf("location %q: want the downloaded v1.2.0 (2 downloads, got %d):\n%s", location, downloads.Load(), out)
		}
	}
}

func TestNoReleasePublishedYet(t *testing.T) {
	base, _ := latestServer(t, "", true)
	for _, args := range [][]string{{"--check"}, nil} {
		exe := installed(t)
		o, _, _ := options(t, "v1.1.0", base, exe)
		err := run(o, args...)
		if err == nil || !strings.Contains(err.Error(), "no release published yet at "+base) ||
			!strings.Contains(err.Error(), "go install github.com/felixn678/agent-mesh/cmd/agent-mesh@latest") {
			t.Fatalf("%v: want friendly no-release error, got %v", args, err)
		}
		var exit ExitError
		if errors.As(err, &exit) {
			t.Fatalf("%v: no release must exit 1, not %d", args, exit.Code)
		}
		mustContent(t, exe, []byte("old binary"))
	}
}
