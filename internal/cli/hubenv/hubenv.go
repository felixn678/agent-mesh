// Package hubenv resolves the hub URL and tokens for every client-side
// command (CLI, hooks, waiter, setup-claude, report), so they all agree.
//
// URL: $AGENTMESH_URL, else hub_url in <config-dir>/client.toml, else in
// <config-dir>/daemon.toml, else the loopback default.
//
// Tokens live in <config-dir> (config.DefaultDir): session.token for
// interactive work, admin.token for cancel/requeue/report.
package hubenv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/config"
)

// DefaultHubURL is the v1 loopback hub.
const DefaultHubURL = "http://127.0.0.1:8787"

// Token file names in the config dir.
const (
	AdminTokenFile   = "admin.token"
	SessionTokenFile = "session.token"
)

// HubURL resolves the hub base URL.
func HubURL() string {
	if u := os.Getenv("AGENTMESH_URL"); u != "" {
		return u
	}
	for _, name := range []string{"client.toml", "daemon.toml"} {
		if u := readHubURL(name); u != "" {
			return u
		}
	}
	return DefaultHubURL
}

// readHubURL reads only hub_url, tolerating other keys (the daemon and the
// onboarding commands own those files' schemas).
func readHubURL(name string) string {
	p, err := File(name)
	if err != nil {
		return ""
	}
	var c struct {
		HubURL string `toml:"hub_url"`
	}
	if _, err := toml.DecodeFile(p, &c); err != nil {
		return ""
	}
	return c.HubURL
}

// File returns <config-dir>/name.
func File(name string) (string, error) {
	dir, err := config.DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func readTokenFile(name string) (tok string, exists bool, err error) {
	p, err := File(name)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(p); err != nil {
		return "", false, nil
	}
	tok, err = config.ReadToken(p)
	return tok, true, err
}

// SessionToken is the host's session token: $AGENTMESH_SESSION_TOKEN, else
// session.token. Hooks, the waiter and the MCP headers helper use only this,
// never $AGENTMESH_TOKEN, which may hold an admin token in a shell and would
// be refused for session endpoints.
func SessionToken() (string, error) {
	if tok := strings.TrimSpace(os.Getenv("AGENTMESH_SESSION_TOKEN")); tok != "" {
		return tok, nil
	}
	tok, ok, err := readTokenFile(SessionTokenFile)
	if err == nil && !ok {
		err = errors.New("no session token: run `agent-mesh up` or `agent-mesh join` (writes session.token in the agent-mesh config dir)")
	}
	return tok, err
}

// ClientToken is for general commands (task add/list/get, hosts):
// $AGENTMESH_TOKEN, else session.token, else admin.token.
func ClientToken() (string, error) {
	return firstToken(SessionTokenFile, AdminTokenFile)
}

// AdminToken is for admin commands (cancel, requeue, report):
// $AGENTMESH_TOKEN, else admin.token, else session.token (read-only
// endpoints accept it; admin-only ones answer 403).
func AdminToken() (string, error) {
	return firstToken(AdminTokenFile, SessionTokenFile)
}

func firstToken(files ...string) (string, error) {
	if tok := strings.TrimSpace(os.Getenv("AGENTMESH_TOKEN")); tok != "" {
		return tok, nil
	}
	for _, f := range files {
		tok, ok, err := readTokenFile(f)
		if ok || err != nil {
			return tok, err
		}
	}
	return "", errors.New("no token: set AGENTMESH_TOKEN or run `agent-mesh up` / `agent-mesh join`")
}

// Client builds a hub client from a token getter.
func Client(token func() (string, error)) (*client.Client, error) {
	tok, err := token()
	if err != nil {
		return nil, err
	}
	return client.New(HubURL(), tok), nil
}
