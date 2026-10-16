// Package hubenv resolves the hub URL and tokens for every client-side
// command (CLI, hooks, waiter, setup-claude, report), so they all agree.
//
// URL: $AGENTMESH_URL, else hub_url in <config-dir>/client.toml, else in
// <config-dir>/daemon.toml, else the loopback default. Session-token clients
// use SessionHubURL, which skips the environment variable.
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
const DefaultHubURL = config.DefaultHubURL

// Token file names in the config dir.
const (
	AdminTokenFile   = "admin.token"
	SessionTokenFile = "session.token"
)

// HubURL resolves the hub base URL against the default config dir.
func HubURL() string {
	dir, _ := config.DefaultDir() // "" only makes the file lookups miss
	u, _ := HubURLIn(dir)
	return u
}

// HubURLIn resolves the hub URL with configDir as the config dir and says
// where it came from; it delegates to config.HubURL so the CLI, report and
// doctor share one resolver.
func HubURLIn(configDir string) (url, source string) {
	return config.HubURL(configDir)
}

// SessionHubURL resolves the hub URL for clients that send the session
// token (hooks, waiter, MCP headers helper, setup-claude): client.toml, else
// daemon.toml, else loopback. It ignores $AGENTMESH_URL on purpose: a
// project's .claude/settings.json "env" can set it for every hook, and the
// session token must only go to the hub the user configured.
func SessionHubURL() string {
	dir, err := config.DefaultDir()
	if err != nil {
		return DefaultHubURL
	}
	for _, name := range []string{"client.toml", "daemon.toml"} {
		var c struct {
			HubURL string `toml:"hub_url"`
		}
		if _, err := toml.DecodeFile(filepath.Join(dir, name), &c); err == nil && c.HubURL != "" {
			return c.HubURL
		}
	}
	return DefaultHubURL
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
