package hub

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/config"
)

// adminName labels the admin principal; no host may use it.
const adminName = "admin"

type authenticator struct {
	mu       sync.RWMutex // guards the tables below; replace swaps them on SIGHUP
	admin    []byte
	hosts    map[string][]byte
	sessions map[string][]byte
}

// newAuthenticator decodes the configured hashes. Entries that fail to decode
// can never match (Config.Validate rejects them at load time).
func newAuthenticator(cfg Config) *authenticator {
	a := &authenticator{hosts: map[string][]byte{}, sessions: map[string][]byte{}}
	a.admin, _ = config.ParseHash(cfg.AdminTokenHash)
	for name, h := range cfg.HostTokens {
		if b, err := config.ParseHash(h); err == nil {
			a.hosts[name] = b
		}
	}
	for name, h := range cfg.SessionTokens {
		if b, err := config.ParseHash(h); err == nil {
			a.sessions[name] = b
		}
	}
	return a
}

// identify hashes the presented token and compares it against every stored
// hash in constant time per comparison, without early exit.
func (a *authenticator) identify(token string) (api.Principal, bool) {
	sum := sha256.Sum256([]byte(token))
	a.mu.RLock()
	defer a.mu.RUnlock()
	var p api.Principal
	found := false
	if len(a.admin) == sha256.Size && subtle.ConstantTimeCompare(sum[:], a.admin) == 1 {
		p, found = api.Principal{Admin: true}, true
	}
	for name, h := range a.hosts {
		if subtle.ConstantTimeCompare(sum[:], h) == 1 {
			p, found = api.Principal{Host: name}, true
		}
	}
	for name, h := range a.sessions {
		if subtle.ConstantTimeCompare(sum[:], h) == 1 {
			p, found = api.Principal{Host: name, Session: true}, true
		}
	}
	return p, found
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// Permission predicates (the matrix is documented in docs and tested in
// permissions_test.go).
func isAdmin(p api.Principal) bool        { return p.Admin }
func isDaemon(p api.Principal) bool       { return !p.Admin && !p.Session }
func isSession(p api.Principal) bool      { return p.Session }
func adminOrSession(p api.Principal) bool { return p.Admin || p.Session }
func anyone(api.Principal) bool           { return true }

// authed wraps a handler needing a principal accepted by allow. The principal
// is also placed in the request context for MCP tool handlers.
func (s *Server) authed(allow func(api.Principal) bool, h func(http.ResponseWriter, *http.Request, api.Principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.auth.identify(bearer(r))
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, api.CodeUnauthorized, "missing or invalid token")
			return
		}
		if !allow(p) {
			writeError(w, http.StatusForbidden, api.CodeForbidden, "token not permitted for this endpoint")
			return
		}
		h(w, r.WithContext(api.WithPrincipal(r.Context(), p)), p)
	}
}

func (s *Server) adminOnly(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return s.authed(isAdmin, func(w http.ResponseWriter, r *http.Request, _ api.Principal) { h(w, r) })
}

func (s *Server) hostOnly(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return s.authed(isDaemon, func(w http.ResponseWriter, r *http.Request, p api.Principal) { h(w, r, p.Host) })
}

// anyToken allows every valid token (read-only endpoints).
func (s *Server) anyToken(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return s.authed(anyone, func(w http.ResponseWriter, r *http.Request, _ api.Principal) { h(w, r) })
}
