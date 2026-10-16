package hub

import (
	"net/http"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/report"
)

const defaultReportWindow = 16 * time.Hour

func (s *Server) addTask(w http.ResponseWriter, r *http.Request, p api.Principal) {
	var spec api.TaskSpec
	if !decode(w, r, &spec) {
		return
	}
	t, replayed, err := s.st.AddTaskReplay(r.Context(), spec, p)
	if err != nil {
		s.fail(w, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK // retry of an add that already landed
	}
	writeJSON(w, status, t)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	switch state {
	case "", api.StateQueued, api.StateRunning, api.StateDone, api.StateStuck, api.StateCancelled:
	default:
		writeError(w, http.StatusBadRequest, api.CodeInvalid, "unknown state filter")
		return
	}
	since, err := parseSince(r.URL.Query().Get("since"), s.st.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	tasks, err := s.st.List(r.Context(), state, since)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	t, err := s.st.Get(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	t, err := s.st.Cancel(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) requeueTask(w http.ResponseWriter, r *http.Request) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	t, err := s.st.Requeue(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.st.ListHosts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hosts)
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	now := s.st.Now()
	since := now.Add(-defaultReportWindow)
	if v := r.URL.Query().Get("since"); v != "" {
		var err error
		if since, err = parseSince(v, now); err != nil {
			s.fail(w, err)
			return
		}
	}
	tasks, err := s.st.ListForReport(r.Context(), since)
	if err != nil {
		s.fail(w, err)
		return
	}
	hosts, err := s.st.ListHosts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write([]byte(report.Build(report.Input{Now: now, Since: since, Tasks: tasks, Hosts: hosts})))
}
