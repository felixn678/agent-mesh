package mcptools

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felixn678/agent-mesh/internal/store"
)

// A reaped session task is not an internal error: the session may finish it.
func TestStoreErrExplainsLeaseReaped(t *testing.T) {
	res := storeErr(store.ErrLeaseReaped)
	text := res.Content[0].(*mcp.TextContent).Text
	if !res.IsError || !strings.Contains(text, "task_done") || strings.Contains(text, "internal error") {
		t.Fatalf("got %q", text)
	}
}
