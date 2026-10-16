package onboard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/felixn678/agent-mesh/internal/config"
)

// ensureDir creates dir with mode 0700 and tightens an existing one.
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod %s: %w", dir, err)
	}
	return nil
}

// writeAtomic writes data to path with the given mode via a temp file and
// rename, so readers never see a partial file and a crash keeps the old one.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// writeIfAbsent creates path (0600) with data unless it exists; it reports
// whether it wrote. Existing files are never touched.
func writeIfAbsent(path string, data []byte) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, f.Close()
}

// ensureToken returns the token stored at path, generating and writing a new
// one (0600) when the file is absent. created reports a fresh token.
func ensureToken(path string) (tok string, created bool, err error) {
	if _, statErr := os.Stat(path); statErr == nil {
		tok, err = config.ReadToken(path)
		return tok, false, err
	}
	if tok, err = config.NewToken(); err != nil {
		return "", false, err
	}
	return tok, true, config.WriteToken(path, tok)
}

// installToken stores tok at path (0600). An existing identical file is kept;
// a different one is an error so a live token is never silently replaced.
func installToken(path, tok string) error {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	if cur, err := os.ReadFile(path); err == nil {
		if strings.TrimSpace(string(cur)) == tok {
			return os.Chmod(path, 0o600)
		}
		return fmt.Errorf("%s already holds a different token; remove it first to re-join", path)
	}
	return config.WriteToken(path, tok)
}

// hasLine reports whether src holds a line that, trimmed, equals want.
func hasLine(src []byte, want string) bool {
	for _, l := range bytes.Split(src, []byte("\n")) {
		if strings.TrimSpace(string(l)) == want {
			return true
		}
	}
	return false
}
