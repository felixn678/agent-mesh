package hub

import (
	"net/http"

	"github.com/felixn678/agent-mesh/internal/api"
)

// touchLoad records piggybacked load; failure must not fail the lease call.
func (s *Server) touchLoad(r *http.Request, host string, l *api.HostLoad) {
	if l == nil {
		return
	}
	if err := s.st.UpsertHostLoad(r.Context(), host, *l); err != nil {
		s.log.Warn("host load not recorded", "host", host, "err", err)
	}
}

func (s *Server) claim(w http.ResponseWriter, r *http.Request, host string) {
	var req api.ClaimRequest
	if !decode(w, r, &req) {
		return
	}
	if err := req.Validate(); err != nil {
		s.fail(w, err)
		return
	}
	s.touchLoad(r, host, req.Load)
	c, err := s.st.Claim(r.Context(), host, req)
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

func (s *Server) hostLoad(w http.ResponseWriter, r *http.Request, host string) {
	var l api.HostLoad
	if !decode(w, r, &l) {
		return
	}
	if err := s.st.UpsertHostLoad(r.Context(), host, l); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request, host string) {
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
	s.touchLoad(r, host, req.Load)
	if err := s.st.Heartbeat(r.Context(), host, id, req.Fence); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.OutcomeResponse{})
}

// outcome runs complete/fail/release with shared decode and response logic.
func outcome[T any](s *Server, w http.ResponseWriter, r *http.Request, host string,
	apply func(host string, id int64, req T) (bool, error)) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	var req T
	if !decode(w, r, &req) {
		return
	}
	again, err := apply(host, id, req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.OutcomeResponse{AlreadyApplied: again})
}

func (s *Server) complete(w http.ResponseWriter, r *http.Request, host string) {
	outcome(s, w, r, host, func(h string, id int64, q api.CompleteRequest) (bool, error) {
		return s.st.Complete(r.Context(), h, id, q)
	})
}

func (s *Server) failTask(w http.ResponseWriter, r *http.Request, host string) {
	outcome(s, w, r, host, func(h string, id int64, q api.FailRequest) (bool, error) {
		return s.st.Fail(r.Context(), h, id, q)
	})
}

func (s *Server) release(w http.ResponseWriter, r *http.Request, host string) {
	outcome(s, w, r, host, func(h string, id int64, q api.ReleaseRequest) (bool, error) {
		return s.st.Release(r.Context(), h, id, q)
	})
}
