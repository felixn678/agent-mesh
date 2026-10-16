package update

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// goManaged reports why exe should be updated with the Go tool instead: it
// was built by `go run` (build cache) or installed by `go install` into
// $GOBIN or $GOPATH/bin.
func (o *Options) goManaged(exe string) bool {
	if strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return true
	}
	dirs := []string{o.GOBIN}
	gopath := o.GOPATH
	if gopath == "" && o.Home != "" {
		gopath = filepath.Join(o.Home, "go")
	}
	for _, p := range filepath.SplitList(gopath) {
		dirs = append(dirs, filepath.Join(p, "bin"))
	}
	for _, d := range dirs {
		if d != "" && sameDir(filepath.Dir(exe), d) {
			return true
		}
	}
	return false
}

func sameDir(a, b string) bool {
	if r, err := filepath.EvalSymlinks(b); err == nil {
		b = r
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// goInstallHint is the command that updates a Go-managed binary.
func goInstallHint(tag string) string {
	if tag == "" {
		tag = "latest"
	}
	return "go install github.com/" + Repo + "/cmd/agent-mesh@" + tag
}

// installerHint is the command that installs release tag into ~/.local/bin.
func installerHint(tag string) string {
	return fmt.Sprintf("curl -fsSL https://raw.githubusercontent.com/%s/main/scripts/install.sh | AGENTMESH_VERSION=%s sh", Repo, tag)
}

// errNotWritable means exe's directory refuses new files.
var errNotWritable = errors.New("not writable")

// replaceBinary atomically replaces exe with src: copy into a temp file in
// exe's directory, chmod 0755, fsync, rename over exe. A running process
// keeps its old inode, so the replacement is safe while agent-mesh runs.
func replaceBinary(src, exe string) (err error) {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".agent-mesh.new.*")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s: %w", dir, errNotWritable)
		}
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(tmp.Name())
		}
	}()
	in, err := os.Open(src)
	if err != nil {
		tmp.Close()
		return err
	}
	_, err = io.Copy(tmp, in)
	in.Close()
	if err == nil {
		err = tmp.Chmod(0o755)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("%s: %w", exe, errNotWritable)
		}
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
