package api

import "regexp"

var requestIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// checkRequestID allows an empty id (no idempotency) or a short token.
func checkRequestID(id string) error {
	if id != "" && !requestIDRE.MatchString(id) {
		return invalid("request_id must match %s", requestIDRE)
	}
	return nil
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
