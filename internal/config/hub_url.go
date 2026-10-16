package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// DefaultHubURL is the v1 loopback hub.
const DefaultHubURL = "http://127.0.0.1:8787"

// HubURL resolves which hub CLI commands talk to: $AGENTMESH_URL, else
// hub_url from client.toml, else from daemon.toml (both in dir), else the
// loopback default. source names where the URL came from.
func HubURL(dir string) (url, source string) {
	if u := os.Getenv("AGENTMESH_URL"); u != "" {
		return u, "$AGENTMESH_URL"
	}
	for _, n := range []string{"client.toml", "daemon.toml"} {
		if dir == "" { // never fall back to files in the current directory
			break
		}
		if u := readHubURL(filepath.Join(dir, n)); u != "" {
			return u, n
		}
	}
	return DefaultHubURL, "default"
}

// readHubURL extracts hub_url from a TOML file, tolerating other keys (the
// daemon validates its own file); "" when absent or unreadable.
func readHubURL(path string) string {
	var c struct {
		HubURL string `toml:"hub_url"`
	}
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return ""
	}
	return c.HubURL
}
