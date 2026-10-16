package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/mcptools"
	"github.com/felixn678/agent-mesh/internal/store"
)

// Server is the hub HTTP server.
type Server struct {
	cfg   Config
	st    *store.Store
	auth  *authenticator
	log   *slog.Logger
	mux   *http.ServeMux
	mcp   http.Handler
	waits *waitLimiter
}

// NewServer wires routes over st. Call Config.Validate first; tokens whose
// hashes do not decode never authenticate.
func NewServer(cfg Config, st *store.Store) *Server {
	s := &Server{cfg: cfg, st: st, auth: newAuthenticator(cfg), log: slog.Default(), mux: http.NewServeMux(),
		mcp: mcptools.NewHandler(st), waits: newWaitLimiter(api.MaxWaitsPerHost)}
	s.routes()
	return s
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	// Admin only: cancel/requeue.
	m.HandleFunc("POST /api/v1/tasks/{id}/cancel", s.adminOnly(s.cancelTask))
	m.HandleFunc("POST /api/v1/tasks/{id}/requeue", s.adminOnly(s.requeueTask))
	// Admin + session tokens: task add, MCP.
	m.HandleFunc("POST /api/v1/tasks", s.authed(adminOrSession, s.addTask))
	m.Handle("/mcp", s.authed(adminOrSession, func(w http.ResponseWriter, r *http.Request, _ api.Principal) {
		s.mcp.ServeHTTP(w, r)
	}))
	// Read-only, any token: tasks, hosts, report.
	m.HandleFunc("GET /api/v1/tasks", s.anyToken(s.listTasks))
	m.HandleFunc("GET /api/v1/tasks/{id}", s.anyToken(s.getTask))
	m.HandleFunc("GET /api/v1/hosts", s.anyToken(s.listHosts))
	m.HandleFunc("GET /api/v1/report", s.anyToken(s.report))
	// Host (daemon) tokens only: lease and load.
	m.HandleFunc("POST /api/v1/claim", s.hostOnly(s.claim))
	m.HandleFunc("POST /api/v1/hosts/load", s.hostOnly(s.hostLoad))
	m.HandleFunc("POST /api/v1/tasks/{id}/heartbeat", s.hostOnly(s.heartbeat))
	m.HandleFunc("POST /api/v1/tasks/{id}/complete", s.hostOnly(s.complete))
	m.HandleFunc("POST /api/v1/tasks/{id}/fail", s.hostOnly(s.failTask))
	m.HandleFunc("POST /api/v1/tasks/{id}/release", s.hostOnly(s.release))
	// Session tokens own sessions and inboxes; admin may list and send.
	m.HandleFunc("POST /api/v1/sessions", s.authed(isSession, s.registerSession))
	m.HandleFunc("POST /api/v1/sessions/{id}/end", s.authed(isSession, s.endSession))
	m.HandleFunc("GET /api/v1/sessions", s.authed(adminOrSession, s.listSessions))
	m.HandleFunc("POST /api/v1/messages", s.authed(adminOrSession, s.sendMessage))
	m.HandleFunc("GET /api/v1/messages/inbox", s.authed(isSession, s.inbox))
	m.HandleFunc("POST /api/v1/messages/ack", s.authed(isSession, s.ackMessages))
	s.routeSessionTasks()
}

// Handler returns the root handler with the body-size limit applied.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		s.mux.ServeHTTP(w, r)
	})
}

// Serve runs on an already-bound listener: grace for leases alive at the last
// reaper tick, then the reaper, then HTTP. Returns nil after a clean shutdown
// triggered by ctx.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	n, err := s.st.Grace(ctx)
	if err != nil {
		return fmt.Errorf("startup grace: %w", err)
	}
	if n > 0 {
		s.log.Info("extended leases after restart", "count", n)
	}
	reapCtx, stopReaper := context.WithCancel(ctx)
	defer stopReaper()
	go s.reapLoop(reapCtx)

	srv := &http.Server{
		Handler:           s.Handler(),
		BaseContext:       func(net.Listener) context.Context { return ctx }, // shutdown ends long polls
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// reapLoop ticks every ReapInterval; the first pass is deferred one interval
// so a crash loop cannot keep advancing last_alive_at.
func (s *Server) reapLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.ReapInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			res, err := s.st.Reap(ctx)
			if err != nil && ctx.Err() == nil {
				s.log.Error("reaper", "err", err)
			} else if res.Stuck+res.Requeued > 0 {
				s.log.Info("reaper", "stuck", res.Stuck, "requeued", res.Requeued)
			}
		}
	}
}
