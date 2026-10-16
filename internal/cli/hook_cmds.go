package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/mcptools/format"
)

// hookTimeout bounds every hook's hub traffic; Claude's own hook timeout (3 s
// in settings) is only a backstop.
const hookTimeout = 800 * time.Millisecond

// hookLogMax caps <state-dir>/hook.log before it is rotated to hook.log.1.
const hookLogMax = 256 * 1024

// hookInput is the subset of Claude's hook stdin JSON we use.
type hookInput struct {
	SessionID      string `json:"session_id"`
	Cwd            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	StopHookActive bool   `json:"stop_hook_active"`
	Reason         string `json:"reason"`
}

// hooksOff reports whether hooks must no-op (set by the daemon for workers).
func hooksOff() bool { return os.Getenv("AGENTMESH_HOOKS") == "off" }

// runHook is the Claude hook entry point. It fails open: any error is logged
// to <state-dir>/hook.log and the hook exits 0 with no output, so a missing
// token or a down hub never disturbs the session.
func runHook(ctx context.Context, std cmdIO, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: hook session-start|prompt|stop|session-end|mcp-headers")
	}
	if args[0] == "mcp-headers" {
		return mcpHeaders(std)
	}
	if hooksOff() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	out, err := hookEvent(ctx, std.in, args[0])
	if err != nil {
		logHook(args[0], err)
		return nil
	}
	if out != nil {
		_, _ = std.out.Write(out)
	}
	return nil
}

// mcpHeaders is the user-scope MCP headersHelper: it prints the bearer header
// from session.token so the token never lands in ~/.claude.json or the env.
func mcpHeaders(std cmdIO) error {
	tok, err := readSessionToken()
	if err != nil {
		return err
	}
	return json.NewEncoder(std.out).Encode(map[string]string{"Authorization": "Bearer " + tok})
}

// hookEvent handles one event and returns the exact stdout bytes (nil = none).
func hookEvent(ctx context.Context, stdin io.Reader, event string) ([]byte, error) {
	var in hookInput
	if err := json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&in); err != nil {
		return nil, fmt.Errorf("decode hook input: %w", err)
	}
	if !api.ValidSessionID(in.SessionID) {
		return nil, fmt.Errorf("invalid session id %q", in.SessionID)
	}
	c, err := sessionClient()
	if err != nil {
		return nil, err
	}
	h := hookCtx{ctx: ctx, c: c, in: in, waiters: waitersDir()}
	switch event {
	case "session-start":
		return h.sessionStart()
	case "prompt":
		return h.prompt()
	case "stop":
		return h.stop()
	case "session-end":
		return nil, c.EndSession(ctx, in.SessionID)
	}
	return nil, fmt.Errorf("unknown hook event %q", event)
}

type hookCtx struct {
	ctx     context.Context
	c       *client.Client
	in      hookInput
	waiters string // pid/marker dir; "" when the state dir is unknown
}

func (h hookCtx) register() (api.Session, error) {
	return h.c.RegisterSession(h.ctx, api.RegisterSessionRequest{
		ID: h.in.SessionID, Cwd: h.in.Cwd, Name: os.Getenv("AGENTMESH_SESSION_NAME")})
}

// unread consumes and renders the session's unread messages ("" if none).
func (h hookCtx) unread() (string, error) {
	msgs, err := h.c.Inbox(h.ctx, h.in.SessionID, true)
	if err != nil || len(msgs) == 0 {
		return "", err
	}
	return format.Messages(msgs, sendersOf(h.ctx, h.c)), nil
}

func (h hookCtx) sessionStart() ([]byte, error) {
	s, err := h.register()
	if err != nil {
		return nil, err
	}
	text := fmt.Sprintf("agentmesh: your session id is %s (%s). Pass it as `from`/`session` to the agentmesh MCP tools. "+
		"To receive messages while idle, start `%s` with run_in_background; when it exits with messages, handle them, "+
		"then restart it. If it says already running, do not restart.", s.ID, s.Host, waiterCommand(s.ID))
	msgs, err := h.unread()
	if err != nil {
		return nil, err
	}
	if msgs != "" {
		text += "\n\n" + msgs
	}
	return contextOutput("SessionStart", text)
}

func (h hookCtx) prompt() ([]byte, error) {
	if _, err := h.register(); err != nil {
		return nil, err
	}
	msgs, err := h.unread()
	if err != nil || msgs == "" {
		return nil, err
	}
	if !waiterAlive(h.waiters, h.in.SessionID) {
		msgs += "\n" + restartReminder(h.in.SessionID)
	}
	return contextOutput("UserPromptSubmit", msgs)
}

// stop blocks the stop once to deliver unread messages, or once per waiter
// death to ask for a restart. stop_hook_active (a stop already continued by
// a hook) always allows, so a session can never loop.
func (h hookCtx) stop() ([]byte, error) {
	if _, err := h.register(); err != nil {
		return nil, err
	}
	if h.in.StopHookActive {
		return nil, nil
	}
	msgs, err := h.unread()
	if err != nil {
		return nil, err
	}
	dead := !waiterAlive(h.waiters, h.in.SessionID)
	switch {
	case msgs != "" && dead:
		msgs += "\n" + restartReminder(h.in.SessionID)
	case msgs == "" && dead && markReminded(h.waiters, h.in.SessionID):
		msgs = restartReminder(h.in.SessionID)
	case msgs == "":
		return nil, nil
	}
	return marshalLine(map[string]string{"decision": "block", "reason": msgs})
}

func contextOutput(event, text string) ([]byte, error) {
	return marshalLine(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": event, "additionalContext": text}})
}

func marshalLine(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(v)
	return b.Bytes(), err
}

func waiterCommand(id string) string {
	return shellQuote(selfBin()) + " inbox --wait --session " + id
}

func restartReminder(id string) string {
	return "[agentmesh] your message waiter is not running; start `" + waiterCommand(id) + "` with run_in_background."
}

// sendersOf indexes sessions by id for message headers (best effort).
func sendersOf(ctx context.Context, c *client.Client) map[string]api.Session {
	all, err := c.ListSessions(ctx, false)
	if err != nil {
		return nil
	}
	m := make(map[string]api.Session, len(all))
	for _, s := range all {
		m[s.ID] = s
	}
	return m
}

// logHook appends one line to <state-dir>/hook.log, rotating past hookLogMax.
// Logging failures are ignored: hooks must stay silent.
func logHook(event string, err error) {
	dir, derr := stateDir()
	if derr != nil || os.MkdirAll(dir, 0o700) != nil {
		return
	}
	p := filepath.Join(dir, "hook.log")
	if st, serr := os.Stat(p); serr == nil && st.Size() > hookLogMax {
		_ = os.Rename(p, p+".1")
	}
	f, ferr := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s: %s\n", time.Now().Format(time.RFC3339), format.Clean(event), format.Clean(err.Error()))
}
