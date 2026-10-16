package mcptools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

// NewServerForTest exposes newServer to the external test package (which
// needs hubtest, and hubtest imports this package through hub).
func NewServerForTest(st *store.Store, p api.Principal) *mcp.Server { return newServer(st, p) }

// AdminOpRefusesNonAdmin checks the defense-in-depth principal check.
func AdminOpRefusesNonAdmin(st *store.Store) bool {
	res, _, _ := tools{st: st, p: api.Principal{Host: "host-a", Session: true}}.adminOp(context.Background(), 1, "x", st.Cancel)
	return res.IsError
}
