// Package mcptools serves the agentmesh MCP tools over Streamable HTTP.
package mcptools

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
	"github.com/felixn678/agent-mesh/internal/version"
)

// NewHandler returns the stateless MCP endpoint backed by st. The hub mounts
// it behind admin-or-session auth and stores the caller in the request
// context; each request gets a server whose tool set matches that principal.
func NewHandler(st *store.Store) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p, ok := api.PrincipalFrom(r.Context())
		if !ok {
			return nil // 400; the hub never mounts this without auth
		}
		return newServer(st, p)
	}, &mcp.StreamableHTTPOptions{Stateless: true})
}

// tools binds the store to one caller.
type tools struct {
	st *store.Store
	p  api.Principal
}

// owner is the store's ownerHost argument: "" (unrestricted) for admin.
func (t tools) owner() string {
	if t.p.Admin {
		return ""
	}
	return t.p.Host
}

// newServer registers the tools the principal may use: admin gets
// cancel/requeue; session tokens get their own inbox and the worker tools.
func newServer(st *store.Store, p api.Principal) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "agentmesh", Version: version.Version}, nil)
	t := tools{st: st, p: p}
	t.addTaskTools(srv)
	t.addHostReportTools(srv)
	t.addSessionTools(srv)
	t.addSessionTaskTools(srv)
	return srv
}

// textResult is a successful tool result with one text block.
func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// errResult is a one-line IsError result.
func errResult(format string, args ...any) *mcp.CallToolResult {
	r := textResult(fmt.Sprintf(format, args...))
	r.IsError = true
	return r
}

// storeErr maps store and validation errors to a one-line IsError result.
// Unexpected errors are logged and reported generically.
func storeErr(err error) *mcp.CallToolResult {
	switch {
	case api.IsValidation(err):
		return errResult("invalid: %v", err)
	case errors.Is(err, store.ErrNotFound):
		return errResult("task not found")
	case errors.Is(err, store.ErrLeaseLost):
		return errResult("lease lost: this session no longer holds the task; stop working on it")
	case errors.Is(err, store.ErrCancelled):
		return errResult("task was cancelled; stop working on it")
	case errors.Is(err, store.ErrConflict):
		return errResult("task state does not allow this operation")
	case errors.Is(err, store.ErrSessionNotFound):
		return errResult("session not found")
	case errors.Is(err, store.ErrSessionOwner):
		return errResult("session belongs to another host; pass your own agentmesh session id")
	case errors.Is(err, store.ErrNotWorker):
		return errResult("this session opted out of worker mode (AGENTMESH_WORKER=off); it does not claim tasks")
	case errors.Is(err, store.ErrSessionEnded):
		return errResult("session has ended")
	case errors.Is(err, store.ErrInboxFull):
		return errResult("recipient inbox is full; try later")
	}
	slog.Error("mcp tool", "err", err)
	return errResult("internal error")
}
