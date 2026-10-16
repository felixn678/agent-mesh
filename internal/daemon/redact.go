package daemon

import (
	"regexp"
	"strings"
)

// secretPatterns cover tokens that could land in stderr tails or reasons
// sent to the hub: GitHub tokens, Anthropic keys, bearer headers, long hex.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{10,}`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`),
}

// redact masks secrets in s.
func redact(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}

// tail returns at most the last n bytes of s, trimmed, starting on a valid
// UTF-8 boundary.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && s[0]&0xC0 == 0x80 {
		s = s[1:]
	}
	return s
}

// short redacts and bounds a message destined for the hub (reason fields).
func short(s string) string { return tail(redact(s), 500) }
