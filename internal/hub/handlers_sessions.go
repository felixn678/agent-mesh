package hub

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
)

// Session endpoints use session tokens: the host comes from the token, never
// the body, and a session may only be touched by the host that registered it.

func (s *Server) registerSession(w http.ResponseWriter, r *http.Request, p api.Principal) {
	var req api.RegisterSessionRequest
	if !decode(w, r, &req) {
		return
	}
	sess, err := s.st.RegisterSession(r.Context(), p.Host, req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) endSession(w http.ResponseWriter, r *http.Request, p api.Principal) {
	if err := s.st.EndSession(r.Context(), p.Host, api.EndSessionRequest{ID: r.PathValue("id")}); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request, _ api.Principal) {
	alive := r.URL.Query().Get("alive")
	list, err := s.st.ListSessions(r.Context(), alive == "1" || alive == "true")
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// sendMessage: a session token may send only from its own host's sessions;
// admin (owner "") may send from any.
func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request, p api.Principal) {
	var req api.SendMessageRequest
	if !decode(w, r, &req) {
		return
	}
	m, err := s.st.SendMessage(r.Context(), p.Host, req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// parseWait accepts "30" (seconds) or "30s"; 0..50 s.
func parseWait(v string) (time.Duration, bool) {
	if v == "" {
		return 0, true
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		secs, aerr := strconv.Atoi(v)
		if aerr != nil {
			return 0, false
		}
		d = time.Duration(secs) * time.Second
	}
	return d, d >= 0 && d <= 50*time.Second
}

func (s *Server) inbox(w http.ResponseWriter, r *http.Request, p api.Principal) {
	q := r.URL.Query()
	mark := q.Get("mark_read")
	wait, ok := parseWait(q.Get("wait"))
	if !ok {
		writeError(w, http.StatusBadRequest, api.CodeInvalid, "wait must be 0..50 seconds")
		return
	}
	if wait > 0 {
		if !s.waits.acquire(p.Host) {
			writeError(w, http.StatusTooManyRequests, api.CodeTooManyWaits, "too many concurrent inbox waits for this host")
			return
		}
		defer s.waits.release(p.Host)
	}
	if c := q.Get("claimable"); c == "1" || c == "true" {
		resp, err := s.st.WaitInboxClaimable(r.Context(), p.Host, q.Get("session"), mark == "1" || mark == "true", wait)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	msgs, err := s.st.WaitInbox(r.Context(), p.Host, q.Get("session"), mark == "1" || mark == "true", wait)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

// ackMessages marks messages the caller has delivered as read (the
// at-least-once alternative to mark_read).
func (s *Server) ackMessages(w http.ResponseWriter, r *http.Request, p api.Principal) {
	var req api.AckMessagesRequest
	if !decode(w, r, &req) {
		return
	}
	n, err := s.st.AckMessages(r.Context(), p.Host, req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.AckMessagesResponse{Acked: n})
}

// waitLimiter caps concurrent long polls per host so they cannot exhaust
// server resources.
type waitLimiter struct {
	mu  sync.Mutex
	max int
	n   map[string]int
}

func newWaitLimiter(max int) *waitLimiter { return &waitLimiter{max: max, n: map[string]int{}} }

func (l *waitLimiter) acquire(host string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n[host] >= l.max {
		return false
	}
	l.n[host]++
	return true
}

func (l *waitLimiter) release(host string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n[host] <= 1 {
		delete(l.n, host)
	} else {
		l.n[host]--
	}
}
