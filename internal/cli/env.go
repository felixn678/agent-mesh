package cli

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/config"
)

// defaultHubURL is the v1 loopback hub.
const defaultHubURL = "http://127.0.0.1:8787"

// Token files in the config dir.
const (
	adminTokenFile   = "admin.token"
	sessionTokenFile = "session.token"
)

// hubURL is $AGENTMESH_URL, else hub_url from <config-dir>/daemon.toml, else
// loopback. daemon.toml is read leniently (only hub_url) because the daemon
// owns its schema.
func hubURL() string {
	if u := os.Getenv("AGENTMESH_URL"); u != "" {
		return u
	}
	if dir, err := config.DefaultDir(); err == nil {
		var d struct {
			HubURL string `toml:"hub_url"`
		}
		if _, err := toml.DecodeFile(filepath.Join(dir, "daemon.toml"), &d); err == nil && d.HubURL != "" {
			return d.HubURL
		}
	}
	return defaultHubURL
}

func configFile(name string) (string, error) {
	dir, err := config.DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// stateDir is $XDG_STATE_HOME/agent-mesh or ~/.local/state/agent-mesh.
func stateDir() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "agent-mesh"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "agent-mesh"), nil
}

// sessionClient authenticates with the per-host session token, the only
// token hooks, the waiter and the user-scope MCP use.
func sessionClient() (*client.Client, error) {
	tok, err := readSessionToken()
	if err != nil {
		return nil, err
	}
	return client.New(hubURL(), tok), nil
}

func readSessionToken() (string, error) {
	p, err := configFile(sessionTokenFile)
	if err != nil {
		return "", err
	}
	return config.ReadToken(p)
}

// cliClient picks the terminal CLI token: $AGENTMESH_TOKEN, else
// admin.token, else session.token.
func cliClient() (*client.Client, error) {
	if tok := os.Getenv("AGENTMESH_TOKEN"); tok != "" {
		return client.New(hubURL(), tok), nil
	}
	for _, name := range []string{adminTokenFile, sessionTokenFile} {
		p, err := configFile(name)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		tok, err := config.ReadToken(p)
		if err != nil {
			return nil, err
		}
		return client.New(hubURL(), tok), nil
	}
	return nil, errors.New("no token: set AGENTMESH_TOKEN or create admin.token / session.token in the agent-mesh config dir")
}

// selfBin is the absolute, symlink-resolved path of this binary, used in
// hook commands and instructions so they work without PATH.
func selfBin() string {
	p, err := os.Executable()
	if err != nil {
		return "agent-mesh"
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
}
