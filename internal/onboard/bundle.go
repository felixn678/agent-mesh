package onboard

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
)

// bundleVersion is the invite format version.
const bundleVersion = 1

var tokenRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Bundle is what `invite` hands to `join`. It holds secrets.
type Bundle struct {
	V            int    `json:"v"`
	URL          string `json:"url"`
	Host         string `json:"host"`
	HostToken    string `json:"host_token"`
	SessionToken string `json:"session_token"`
}

// Encode renders the bundle as unpadded base64url JSON (safe to paste).
func (b Bundle) Encode() (string, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("encode invite: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeBundle parses and validates an invite; hub input is untrusted.
func DecodeBundle(s string) (Bundle, error) {
	var b Bundle
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return b, errors.New("invite bundle is not valid base64url (copy the whole line)")
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return b, errors.New("invite bundle is not valid JSON")
	}
	switch {
	case b.V != bundleVersion:
		return b, fmt.Errorf("unsupported invite version %d (update agent-mesh)", b.V)
	case checkHostName(b.Host) != nil:
		return b, checkHostName(b.Host)
	case !tokenRE.MatchString(b.HostToken) || !tokenRE.MatchString(b.SessionToken):
		return b, errors.New("invite bundle holds malformed tokens")
	}
	if _, err := ParseHubURL(b.URL); err != nil {
		return b, err
	}
	return b, nil
}

// ParseHubURL accepts only absolute http(s) URLs without credentials, query
// or path, and returns them without a trailing slash.
func ParseHubURL(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("invalid hub url %q: expected http://host:port", s)
	}
	return u.Scheme + "://" + u.Host, nil
}
