package hub

import (
	"net/http"

	"github.com/felixn678/agent-mesh/internal/api"
)

// listHostProfiles returns every machine's role note, detected tools and live
// session count (any token: sessions read it to choose target_host).
func (s *Server) listHostProfiles(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListHostProfiles(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// setHostNote sets a machine's role note: a session token only for its own
// machine, the admin token for any.
func (s *Server) setHostNote(w http.ResponseWriter, r *http.Request, p api.Principal) {
	var req api.HostNoteRequest
	if !decode(w, r, &req) {
		return
	}
	owner := p.Host
	if p.Admin {
		owner = ""
	}
	if err := s.st.SetHostNote(r.Context(), owner, r.PathValue("name"), req); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
