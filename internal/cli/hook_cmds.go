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
	"github.com/felixn678/agent-mesh/internal/version"
)

// hookTimeout bounds every hook's hub traffic; Claude's own hook timeout (3 s
// in settings) is only a backstop.
const hookTimeout = 800 * time.Millisecond

// ackTimeout bounds the ack sent after a hook's output is written.
const ackTimeout = 500 * time.Millisecond

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
	// Commands written by setup-claude carry a trailing ownership marker.
	if len(args) == 2 && args[1] == managedFlag {
		args = args[:1]
	}
	if len(args) != 1 {
		return errors.New("usage: hook session-start|prompt|stop|session-end|mcp-headers")
	}
	if args[0] == "mcp-headers" {
		return mcpHeaders(std)
	}
	if hooksOff() {
		return nil
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	out, h, err := hookEvent(ctx, std.in, args[0])
	if err != nil {
		logHook(args[0], err)
		return nil
	}
	if out == nil {
		return nil
	}
	if _, err := std.out.Write(out); err != nil {
		logHook(args[0], err)
		return nil // not acked: the messages are delivered again next time
	}
	if len(h.shown) > 0 {
		// Own budget: a slow hub must not starve the ack (else the same
		// messages are injected on every prompt).
		ackCtx, cancelAck := context.WithTimeout(parent, ackTimeout)
		defer cancelAck()
		if _, err := h.c.AckMessages(ackCtx, h.in.SessionID, h.shown); err != nil {
			logHook(args[0]+" ack", err) // at-least-once: they will show again
		}
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

// hookEvent handles one event and returns the exact stdout bytes (nil = none)
// plus the hook state holding the message ids to ack once they are written.
func hookEvent(ctx context.Context, stdin io.Reader, event string) ([]byte, *hookCtx, error) {
	var in hookInput
	if err := json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&in); err != nil {
		return nil, nil, fmt.Errorf("decode hook input: %w", err)
	}
	if !api.ValidSessionID(in.SessionID) {
		return nil, nil, fmt.Errorf("invalid session id %q", in.SessionID)
	}
	c, err := sessionClient()
	if err != nil {
		return nil, nil, err
	}
	h := &hookCtx{ctx: ctx, c: c, in: in, waiters: waitersDir()}
	var out []byte
	switch event {
	case "session-start":
		out, err = h.sessionStart()
	case "prompt":
		out, err = h.prompt()
	case "stop":
		out, err = h.stop()
	case "session-end":
		// End first (one cheap call), then hand back held tasks; both stay
		// within the hook's time budget and fail open.
		err = errors.Join(c.EndSession(ctx, in.SessionID), releaseHeld(ctx, c, in.SessionID))
	default:
		err = fmt.Errorf("unknown hook event %q", event)
	}
	return out, h, err
}

type hookCtx struct {
	ctx     context.Context
	c       *client.Client
	in      hookInput
	waiters string  // pid/marker dir; "" when the state dir is unknown
	shown   []int64 // message ids rendered into the output, acked after it is written
}

// register refreshes the session and reports this binary's version; repo is
// sent only at session start (an empty repo keeps the stored one).
func (h *hookCtx) register(repo string, worker *bool) (api.Session, error) {
	return h.c.RegisterSession(h.ctx, api.RegisterSessionRequest{
		ID: h.in.SessionID, Cwd: h.in.Cwd, Name: os.Getenv("AGENTMESH_SESSION_NAME"), Repo: repo, Worker: worker,
		AgentVersion: version.Version})
}

// unread renders the session's unread messages ("" if none) without marking
// them read; the rendered ids are acked after the output is written.
func (h *hookCtx) unread() (string, error) {
	msgs, err := h.c.Inbox(h.ctx, h.in.SessionID, false)
	if err != nil || len(msgs) == 0 {
		return "", err
	}
	text, n := format.Messages(msgs, sendersOf(h.ctx, h.c))
	h.shown = format.IDs(msgs[:n])
	return text, nil
}

func (h *hookCtx) sessionStart() ([]byte, error) {
	worker := workerMode()
	s, err := h.register(gitRemote(h.ctx, h.in.Cwd), &worker)
	if err != nil {
		return nil, err
	}
	text := sessionStartText(s, worker)
	if skew := version.LocalSkew(version.Version, h.c.HubVersion()); skew != "" {
		text += "\n[agentmesh] " + skew + " (tell the user once)."
	}
	msgs, err := h.unread()
	if err != nil {
		return nil, err
	}
	if msgs != "" {
		text += "\n\n" + msgs
	}
	return contextOutput("SessionStart", text)
}

func (h *hookCtx) prompt() ([]byte, error) {
	if _, err := h.register("", nil); err != nil {
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

// stop extends leases of tasks this session holds, then blocks the stop once to deliver unread messages, or once per waiter
// death to ask for a restart. stop_hook_active (a stop already continued by
// a hook) always allows, so a session can never loop.
func (h *hookCtx) stop() ([]byte, error) {
	if _, err := h.register("", nil); err != nil {
		return nil, err
	}
	if err := heartbeatHeld(h.ctx, h.c, h.in.SessionID); err != nil {
		logHook("stop heartbeat", err) // keep delivering messages
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
