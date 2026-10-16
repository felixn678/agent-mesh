package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxArchive = 256 << 20 // release archives are ~10 MB
	maxSums    = 1 << 20
	maxBinary  = 512 << 20
)

// release is a downloaded, verified and unpacked release binary.
type release struct {
	Path    string // unpacked binary in a private temp dir
	Version string // as stamped into the binary at build time
}

// releaseBase is the directory URL holding the assets of tag ("" = latest).
func (o *Options) releaseBase(tag string) string {
	switch {
	case o.BaseURL != "":
		return strings.TrimRight(o.BaseURL, "/")
	case tag == "":
		return "https://github.com/" + Repo + "/releases/latest/download"
	}
	return "https://github.com/" + Repo + "/releases/download/" + tag
}

// fetch downloads base/name, at most limit bytes. GitHub downloads are
// HTTPS-only, redirects included; an AGENTMESH_DOWNLOAD_BASE override may
// also use http:// or file:// (mirrors and tests), like the installer.
func (o *Options) fetch(ctx context.Context, base, name string, limit int64) ([]byte, error) {
	raw := base + "/" + name
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", raw, err)
	}
	if u.Scheme == "file" {
		return readLimited(raw, limit, func() (io.ReadCloser, error) { return os.Open(u.Path) })
	}
	if !o.schemeAllowed(u.Scheme) {
		return nil, fmt.Errorf("download %s: only https URLs are allowed", raw)
	}
	client := *o.HTTP
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("refusing redirect from https to %s", req.URL.Scheme)
		}
		return nil
	}
	return readLimited(raw, limit, func() (io.ReadCloser, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				return nil, fmt.Errorf("HTTP %s: %w", resp.Status, errNotFound)
			}
			return nil, fmt.Errorf("HTTP %s", resp.Status)
		}
		return resp.Body, nil
	})
}

// schemeAllowed: GitHub is HTTPS-only; a download-base override may also be
// http:// or file://.
func (o *Options) schemeAllowed(scheme string) bool {
	return scheme == "https" || o.BaseURL != "" && (scheme == "http" || scheme == "file")
}

func readLimited(what string, limit int64, open func() (io.ReadCloser, error)) ([]byte, error) {
	rc, err := open()
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", what, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", what, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("download %s: larger than %d bytes", what, limit)
	}
	return b, nil
}

// checksumFor finds asset's SHA-256 in a SHA256SUMS file ("<hex>  <name>",
// "*<name>" in binary mode).
func checksumFor(sums []byte, asset string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && (f[1] == asset || f[1] == "*"+asset) {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS has no entry for %s", asset)
}

// download fetches the release asset and SHA256SUMS, refuses a checksum
// mismatch, unpacks the binary into dir and reads the version stamped into it.
func (o *Options) download(ctx context.Context, tag, dir string) (*release, error) {
	base, asset := o.releaseBase(tag), o.asset()
	sums, err := o.fetch(ctx, base, "SHA256SUMS", maxSums)
	if tag == "" && errors.Is(err, errNotFound) {
		return nil, noRelease(base)
	}
	if err != nil {
		return nil, err
	}
	want, err := checksumFor(sums, asset)
	if err != nil {
		return nil, err
	}
	archive, err := o.fetch(ctx, base, asset, maxArchive)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("checksum mismatch for %s (expected %s, got %x); not installing", asset, want, got)
	}
	path := filepath.Join(dir, "agent-mesh")
	if err := unpackBinary(archive, path); err != nil {
		return nil, fmt.Errorf("unpack %s: %w", asset, err)
	}
	v, err := o.releaseVersion(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", asset, err)
	}
	return &release{Path: path, Version: v}, nil
}

// unpackBinary writes the regular file "agent-mesh" from a .tar.gz to dst.
func unpackBinary(archive []byte, dst string) error {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return errors.New("archive has no agent-mesh binary")
		}
		if err != nil {
			return err
		}
		if h.Name != "agent-mesh" && h.Name != "./agent-mesh" || h.Typeflag != tar.TypeReg {
			continue
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, io.LimitReader(tr, maxBinary+1))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil && n > maxBinary {
			err = errors.New("binary too large")
		}
		return err
	}
}

// releaseVersion checks from the build info that the binary was built for
// this OS and CPU, then runs `<binary> version`: release builds stamp the
// version with -X, which -trimpath keeps out of the build info. The archive
// already passed the checksum, and it is the binary about to be installed.
func (o *Options) releaseVersion(ctx context.Context, path string) (string, error) {
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("not a Go binary: %w", err)
	}
	for _, s := range bi.Settings {
		if want := map[string]string{"GOOS": o.GOOS, "GOARCH": o.GOARCH}[s.Key]; want != "" && s.Value != want {
			return "", fmt.Errorf("binary is built for %s=%s, this machine needs %s", s.Key, s.Value, want)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", fmt.Errorf("run the new binary: %w", err)
	}
	v := strings.TrimSpace(string(out))
	if isDevBuild(v) {
		return "", fmt.Errorf("binary reports %q, not a release version", v)
	}
	return v, nil
}
