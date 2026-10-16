package api

import (
	"regexp"
	"time"
	"unicode/utf8"
)

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
