package onboard

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/felixn678/agent-mesh/internal/hub"
)

// editHubTOML applies fn to the text of hub.toml and installs the result only
// if the real hub loader accepts it, so a bad edit can never break a running
// setup. The file keeps mode 0600 and all untouched text (comments included).
func editHubTOML(path string, fn func(src string) (string, error)) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	next, err := fn(string(src))
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-hub-")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(next); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if _, err := hub.LoadConfig(tmp.Name()); err != nil {
		return fmt.Errorf("refusing to write an invalid hub.toml: %w", err)
	}
	return writeAtomic(path, []byte(next), 0o600)
}
