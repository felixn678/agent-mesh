package api

import (
	"regexp"
	"time"
	"unicode/utf8"
)

// Session registry and message wire types.

// Session is a registered Claude Code session. Alive is computed by the hub:
// not ended and seen within SessionAliveWindow.
type Session struct {
	ID         string `json:"id"`
	Host       string `json:"host"`
	Cwd        string `json:"cwd,omitempty"`
	Name       string `json:"name,omitempty"`
	Repo       string `json:"repo,omitempty"` // normalized project key
	StartedAt  int64  `json:"started_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	EndedAt    int64  `json:"ended_at,omitempty"`
	Alive      bool   `json:"alive"`
}

// RegisterSessionRequest registers or refreshes (heartbeat) a session.
type RegisterSessionRequest struct {
	ID   string `json:"id"`
	Cwd  string `json:"cwd,omitempty"`
	Name string `json:"name,omitempty"`
	Repo string `json:"repo,omitempty"` // git remote URL or key; normalized by the hub; empty keeps the stored value
}

// EndSessionRequest marks a session ended.
type EndSessionRequest struct {
	ID string `json:"id"`
}

// SendMessageRequest sends an opaque body to one exact session id.
type SendMessageRequest struct {
	FromSession string `json:"from_session"`
	ToSession   string `json:"to_session"`
	Body        string `json:"body"`
}

// Message is a stored session message.
type Message struct {
	ID          int64  `json:"id"`
	FromSession string `json:"from_session"`
	FromHost    string `json:"from_host"`
	ToSession   string `json:"to_session"`
	Body        string `json:"body"`
	CreatedAt   int64  `json:"created_at"`
	ReadAt      int64  `json:"read_at,omitempty"`
}

// InboxResponse is the body of an inbox poll with claimable=1: unread
// messages plus how many queued tasks this session could claim right now.
type InboxResponse struct {
	Messages       []Message `json:"messages"`
	ClaimableCount int       `json:"claimable_count"`
}

// SessionClaimRequest is the optional body of a session claim. RequestID has
// the same retry meaning as ClaimRequest.RequestID.
type SessionClaimRequest struct {
	RequestID string `json:"request_id,omitempty"`
}

// Validate checks the request id.
func (r SessionClaimRequest) Validate() error { return checkRequestID(r.RequestID) }

// AckMessagesRequest marks delivered messages read. Fetching the inbox
// without mark_read and acking what was actually shown gives at-least-once
// delivery: a response lost in transit leaves the messages unread.
type AckMessagesRequest struct {
	Session string  `json:"session"`
	IDs     []int64 `json:"ids"`
}

// AckMessagesResponse reports how many messages changed from unread to read;
// ids already read or addressed to another session are ignored.
type AckMessagesResponse struct {
	Acked int64 `json:"acked"`
}

// Validate checks the session id and bounds the id list.
func (r AckMessagesRequest) Validate() error {
	if !ValidSessionID(r.Session) {
		return invalid("session must match %s", sessionIDRE)
	}
	if len(r.IDs) == 0 || len(r.IDs) > MaxUnreadPerSession {
		return invalid("ids must list 1..%d message ids", MaxUnreadPerSession)
	}
	for _, id := range r.IDs {
		if id < 1 {
			return invalid("message ids must be positive")
		}
	}
	return nil
}

// Session and message limits.
const (
	MaxMessageBytes     = 8 * 1024
	SessionAliveWindow  = 12 * time.Hour // default; idle sessions only touch last_seen on prompt/stop
	MaxUnreadPerSession = 100
	MaxWaitsPerHost     = 50
	SessionListLimit    = 500
	InboxLimit          = 50
)

var sessionIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// ValidSessionID checks a Claude session id.
func ValidSessionID(s string) bool { return sessionIDRE.MatchString(s) }

// Validate checks a session registration.
func (r RegisterSessionRequest) Validate() error {
	if !ValidSessionID(r.ID) {
		return invalid("session id must match %s", sessionIDRE)
	}
	if len(r.Cwd) > 500 || !utf8.ValidString(r.Cwd) {
		return invalid("cwd must be valid UTF-8 of at most 500 bytes")
	}
	if len(r.Name) > 100 || !utf8.ValidString(r.Name) {
		return invalid("name must be valid UTF-8 of at most 100 bytes")
	}
	if r.Repo != "" {
		if _, err := NormalizeRepo(r.Repo); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks an end request.
func (r EndSessionRequest) Validate() error {
	if !ValidSessionID(r.ID) {
		return invalid("session id must match %s", sessionIDRE)
	}
	return nil
}

// Validate checks a message. The body is opaque but bounded UTF-8.
func (r SendMessageRequest) Validate() error {
	if !ValidSessionID(r.FromSession) || !ValidSessionID(r.ToSession) {
		return invalid("from_session and to_session must match %s", sessionIDRE)
	}
	if r.Body == "" {
		return invalid("body must not be empty")
	}
	if len(r.Body) > MaxMessageBytes || !utf8.ValidString(r.Body) {
		return invalid("body must be valid UTF-8 of at most %d bytes", MaxMessageBytes)
	}
	return nil
}
