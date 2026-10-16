package api

// WhoAmI is the GET /api/v1/whoami response: what kind of token the caller
// presented. Kind is "admin", "host" (daemon) or "session".
type WhoAmI struct {
	Kind string `json:"kind"`
	Host string `json:"host,omitempty"`
	// HubVersion is the hub's agent-mesh version (empty from older hubs).
	HubVersion string `json:"hub_version,omitempty"`
}

// WhoAmIFor describes p.
func WhoAmIFor(p Principal) WhoAmI {
	switch {
	case p.Admin:
		return WhoAmI{Kind: "admin"}
	case p.Session:
		return WhoAmI{Kind: "session", Host: p.Host}
	}
	return WhoAmI{Kind: "host", Host: p.Host}
}
