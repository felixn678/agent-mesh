package hub

import (
	"net/http"

	"github.com/felixn678/agent-mesh/internal/api"
)

// Session-held tasks. The path session must belong to the token's host; the
// lease holder is derived from it ("session:<id>"), so a session can only
// act on tasks it claimed itself.

func (s *Server) sessionHolder(w http.ResponseWriter, r *http.Request, p api.Principal) (string, bool) {
	holder, err := s.st.SessionHolder(r.Context(), p.Host, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return "", false
	}
	return holder, true
}

// sessionClaim accepts an empty body or {"request_id": ...}.
func (s *Server) sessionClaim(w http.ResponseWriter, r *http.Request, p api.Principal) {
	var req api.SessionClaimRequest
	if !decodeOptional(w, r, &req) {
		return
	}
	c, err := s.st.ClaimForSessionRequest(r.Context(), p.Host, r.PathValue("id"), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	if c == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// sessionLease adapts a holder-based lease handler to the session routes.
func (s *Server) sessionLease(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return s.authed(isSession, func(w http.ResponseWriter, r *http.Request, p api.Principal) {
		if holder, ok := s.sessionHolder(w, r, p); ok {
			h(w, r, holder)
		}
	})
}

func (s *Server) routeSessionTasks() {
	m := s.mux
	m.HandleFunc("POST /api/v1/sessions/{id}/claim", s.authed(isSession, s.sessionClaim))
	m.HandleFunc("POST /api/v1/sessions/{id}/tasks/{tid}/heartbeat", s.sessionLease(s.sessionHeartbeat))
	m.HandleFunc("POST /api/v1/sessions/{id}/tasks/{tid}/complete", s.sessionLease(s.complete))
	m.HandleFunc("POST /api/v1/sessions/{id}/tasks/{tid}/fail", s.sessionLease(s.failTask))
	m.HandleFunc("POST /api/v1/sessions/{id}/tasks/{tid}/release", s.sessionLease(s.release))
}

func (s *Server) sessionHeartbeat(w http.ResponseWriter, r *http.Request, holder string) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	var req api.HeartbeatRequest
	if !decode(w, r, &req) {
		return
	}
	if err := req.Validate(); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.st.Heartbeat(r.Context(), holder, id, req.Fence); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.OutcomeResponse{})
}
