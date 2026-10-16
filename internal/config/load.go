// Package config holds TOML loading and token helpers shared by the hub,
// daemon and CLI.
package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// HashPrefix tags stored token hashes.
const HashPrefix = "sha256:"

// LoadTOML decodes path into v and rejects unknown keys (typos in a security
// config should fail loudly).
func LoadTOML(path string, v any) error {
	md, err := toml.DecodeFile(path, v)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return fmt.Errorf("parse %s: unknown keys %v", path, u)
	}
	return nil
}

// DefaultDir is the per-user config directory: ~/.config/agent-mesh on Linux
// ($XDG_CONFIG_HOME honoured), ~/Library/Application Support/agent-mesh on
// macOS (os.UserConfigDir). Note the space in the macOS path: never build
// shell strings from it.
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config dir: %w", err)
	}
	return filepath.Join(base, "agent-mesh"), nil
}

// ExpandHome replaces a leading "~/" with the user's home directory.
func ExpandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %q: %w", p, err)
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}

// NewToken returns a fresh 32-byte random token as 64 hex characters.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// HashToken returns "sha256:<hex>" of the token, the form stored in hub.toml.
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return HashPrefix + hex.EncodeToString(sum[:])
}

// ParseHash decodes a "sha256:<hex>" value into its 32 raw bytes.
func ParseHash(s string) ([]byte, error) {
	raw, ok := strings.CutPrefix(s, HashPrefix)
	if !ok {
		return nil, fmt.Errorf("token hash must start with %q", HashPrefix)
	}
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) != sha256.Size {
		return nil, errors.New("token hash must be 64 hex characters")
	}
	return b, nil
}

// WriteToken creates path (mode 0600, parents 0700) holding tok. It refuses
// to overwrite an existing file so a live token is never clobbered.
func WriteToken(path, tok string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create token dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create token file: %w", err)
	}
	if _, err := f.WriteString(tok + "\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("write token: %w", err)
	}
	return f.Close()
}

// ReadToken reads a token file, refusing group/world-accessible files.
func ReadToken(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("token file %s has mode %v; require 0600", path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return tok, nil
}
