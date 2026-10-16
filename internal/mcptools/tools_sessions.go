package mcptools

import (
	"context"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/mcptools/format"
)

// maxSessionLines bounds the sessions tool output.
const maxSessionLines = 50

// SendIn is the send input. To may be a full id, a unique id prefix of at
// least 4 characters, or a unique session name.
type SendIn struct {
	From string `json:"from" jsonschema:"your agentmesh session id (from session start context)"`
	To   string `json:"to" jsonschema:"session id, unique prefix, or name"`
	Text string `json:"text" jsonschema:"≤8KB"`
}

// InboxIn names the caller's own session.
type InboxIn struct {
	Session string `json:"session" jsonschema:"your agentmesh session id"`
}

func (t tools) addSessionTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{Name: "sessions", Description: "Live Claude sessions on all hosts."}, t.sessions)
	mcp.AddTool(srv, &mcp.Tool{Name: "send", Description: "Message another Claude session."}, t.send)
	if t.p.Session {
		mcp.AddTool(srv, &mcp.Tool{Name: "inbox", Description: "Read (and consume) your unread messages."}, t.inbox)
	}
}

func (t tools) sessions(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	list, err := t.st.ListSessions(ctx, true)
	if err != nil {
		return storeErr(err), nil, nil
	}
	if len(list) == 0 {
		return textResult("no live sessions"), nil, nil
	}
	now := t.st.Now()
	var lines []string
	for i, s := range list {
		if i == maxSessionLines {
			lines = append(lines, "…")
			break
		}
		lines = append(lines, format.SessionLine(s, now))
	}
	return textResult(strings.Join(lines, "\n")), nil, nil
}

func (t tools) send(ctx context.Context, _ *mcp.CallToolRequest, in SendIn) (*mcp.CallToolResult, any, error) {
	to, errRes := t.resolveTarget(ctx, in.To)
	if errRes != nil {
		return errRes, nil, nil
	}
	m, err := t.st.SendMessage(ctx, t.owner(), api.SendMessageRequest{FromSession: in.From, ToSession: to.ID, Body: in.Text})
	if err != nil {
		return storeErr(err), nil, nil
	}
	return textResult("sent message " + strconv.FormatInt(m.ID, 10) + " to " + format.SessionLine(to, t.st.Now())), nil, nil
}

// resolveTarget maps a full id, unique prefix (≥4 chars) or unique name to a
// session. Exact ids win; prefixes and names only match live sessions.
func (t tools) resolveTarget(ctx context.Context, to string) (api.Session, *mcp.CallToolResult) {
	if to == "" {
		return api.Session{}, errResult("to is required")
	}
	all, err := t.st.ListSessions(ctx, false)
	if err != nil {
		return api.Session{}, storeErr(err)
	}
	var cands []api.Session
	for _, s := range all {
		if s.ID == to {
			return s, nil
		}
		if s.Alive && ((len(to) >= 4 && strings.HasPrefix(s.ID, to)) || s.Name == to) {
			cands = append(cands, s)
		}
	}
	switch len(cands) {
	case 0:
		return api.Session{}, errResult("no live session matches %q (use an id, a 4+ char prefix, or a name)", to)
	case 1:
		return cands[0], nil
	}
	now := t.st.Now()
	lines := make([]string, len(cands))
	for i, s := range cands {
		lines[i] = format.SessionLine(s, now)
	}
	return api.Session{}, errResult("%q is ambiguous; candidates:\n%s", to, strings.Join(lines, "\n"))
}

func (t tools) inbox(ctx context.Context, _ *mcp.CallToolRequest, in InboxIn) (*mcp.CallToolResult, any, error) {
	if !t.p.Session {
		return errResult("session token required"), nil, nil
	}
	msgs, err := t.st.Inbox(ctx, t.owner(), in.Session, false)
	if err != nil {
		return storeErr(err), nil, nil
	}
	if len(msgs) == 0 {
		return textResult("no unread messages"), nil, nil
	}
	text, n := format.Messages(msgs, t.senders(ctx))
	// Ack only what was rendered, as late as the tool can (just before the
	// result is returned).
	if _, err := t.st.AckMessages(ctx, t.owner(), api.AckMessagesRequest{Session: in.Session, IDs: format.IDs(msgs[:n])}); err != nil {
		return storeErr(err), nil, nil
	}
	return textResult(text), nil, nil
}

// senders indexes all sessions by id for message headers (best effort).
func (t tools) senders(ctx context.Context) map[string]api.Session {
	all, err := t.st.ListSessions(ctx, false)
	if err != nil {
		return nil
	}
	m := make(map[string]api.Session, len(all))
	for _, s := range all {
		m[s.ID] = s
	}
	return m
}
