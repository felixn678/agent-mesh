// Package mcptools serves the agentmesh MCP tools over Streamable HTTP.
package mcptools

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felixn678/agent-mesh/internal/store"
	"github.com/felixn678/agent-mesh/internal/version"
)

// NewHandler returns the stateless MCP endpoint backed by st. The hub mounts
// it behind admin auth. This skeleton registers no tools yet.
func NewHandler(st *store.Store) http.Handler {
	_ = st
	srv := mcp.NewServer(&mcp.Implementation{Name: "agentmesh", Version: version.Version}, nil)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true})
}
