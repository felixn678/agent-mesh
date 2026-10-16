package cli

import (
	"os"
	"path/filepath"

	"github.com/felixn678/agent-mesh/internal/cli/hubenv"
	"github.com/felixn678/agent-mesh/internal/client"
)

func hubURL() string { return hubenv.HubURL() }

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

// sessionClient authenticates with the host's session token, the only token
// hooks, the waiter and the user-scope MCP use.
func sessionClient() (*client.Client, error) { return hubenv.Client(hubenv.SessionToken) }

func readSessionToken() (string, error) { return hubenv.SessionToken() }

// cliClient is for task add/list/get and hosts; adminClient for cancel/requeue.
func cliClient() (*client.Client, error)   { return hubenv.Client(hubenv.ClientToken) }
func adminClient() (*client.Client, error) { return hubenv.Client(hubenv.AdminToken) }

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
