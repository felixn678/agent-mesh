package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/store"
)

// maxBodyBytes bounds every request body.
const maxBodyBytes = 128 * 1024

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // client gone is not actionable
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, api.ErrorBody{Error: msg, Code: code})
}

// decode reads a JSON body (already size-limited). On failure it writes the
// response (400, or 413 past the limit) and returns false.
func decode(w http.ResponseWriter, r *http.Request, v any) bool { return decodeBody(w, r, v, false) }

// decodeOptional is decode that also accepts an empty body (zero value).
func decodeOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeBody(w, r, v, true)
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any, allowEmpty bool) bool {
	err := json.NewDecoder(r.Body).Decode(v)
	if err == nil || (allowEmpty && errors.Is(err, io.EOF)) {
		return true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, api.CodeTooLarge, "request body too large")
	} else {
		writeError(w, http.StatusBadRequest, api.CodeInvalid, "malformed JSON body")
	}
	return false
}

// fail maps store/validation errors to HTTP responses.
func (s *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case api.IsValidation(err):
		writeError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrSessionNotFound):
		writeError(w, http.StatusNotFound, api.CodeNotFound, err.Error())
	case errors.Is(err, store.ErrLeaseLost):
		writeError(w, http.StatusConflict, api.CodeLeaseLost, err.Error())
	case errors.Is(err, store.ErrLeaseReaped):
		writeError(w, http.StatusConflict, api.CodeLeaseReaped, err.Error())
	case errors.Is(err, store.ErrCancelled):
		writeError(w, http.StatusConflict, api.CodeCancelled, err.Error())
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, api.CodeConflict, err.Error())
	case errors.Is(err, store.ErrSessionEnded):
		writeError(w, http.StatusConflict, api.CodeEnded, err.Error())
	case errors.Is(err, store.ErrInboxFull):
		writeError(w, http.StatusConflict, api.CodeInboxFull, err.Error())
	case errors.Is(err, store.ErrSessionOwner):
		writeError(w, http.StatusForbidden, api.CodeForbidden, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// Client went away or the hub is shutting down; not a server fault.
		writeError(w, http.StatusServiceUnavailable, api.CodeUnavailable, "request cancelled")
	default:
		s.log.Error("internal error", "err", err)
		writeError(w, http.StatusInternalServerError, api.CodeInternal, "internal error")
	}
}

func taskID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("tid") // session task routes carry the session in {id}
	if raw == "" {
		raw = r.PathValue("id")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, api.CodeInvalid, "invalid task id")
		return 0, false
	}
	return id, true
}

// parseSince accepts "" (zero time), a Go duration before now ("16h") or an
// RFC 3339 timestamp.
func parseSince(v string, now time.Time) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(v); err == nil {
		if d < 0 {
			return time.Time{}, &api.ValidationError{Msg: "since duration must be positive"}
		}
		return now.Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, &api.ValidationError{Msg: "since must be a duration like 16h or an RFC 3339 time"}
	}
	return t, nil
}
