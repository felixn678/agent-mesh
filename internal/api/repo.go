package api

import (
	"regexp"
	"strings"
)

const maxRepoKeyLen = 200

var (
	repoSegRE    = regexp.MustCompile(`^[a-z0-9._-]+$`)
	schemeRE     = regexp.MustCompile(`^[a-z][a-z0-9+.-]*://`)
	portPrefixRE = regexp.MustCompile(`^([^/:]+):[0-9]+(/|$)`)
)

// NormalizeRepo turns a git remote URL into the project key shared by every
// clone: lowercase host/owner/name without scheme, user, port or the ".git"
// suffix. https://User@GitHub.com/Owner/Name.git, ssh://git@github.com:22/owner/name
// and git@github.com:owner/name all become "github.com/owner/name".
func NormalizeRepo(s string) (string, error) {
	r := strings.ToLower(strings.TrimSpace(s))
	if r == "" || strings.ContainsAny(r, "?#\\ \t\r\n") {
		return "", invalid("repo remote must be a git URL such as github.com/owner/name")
	}
	hadScheme := schemeRE.MatchString(r)
	r = schemeRE.ReplaceAllString(r, "")
	if at := strings.Index(r, "@"); at >= 0 && !strings.Contains(r[:at], "/") {
		r = r[at+1:] // user[:password]@
	}
	if hadScheme {
		r = portPrefixRE.ReplaceAllString(r, "$1$2")
	} else if i := strings.Index(r, ":"); i >= 0 && !strings.Contains(r[:i], "/") {
		r = r[:i] + "/" + r[i+1:] // scp-like host:owner/name
	}
	r = strings.TrimRight(r, "/")
	r = strings.TrimSuffix(r, ".git")
	r = strings.TrimRight(r, "/")
	segs := strings.Split(r, "/")
	if len(r) > maxRepoKeyLen || len(segs) < 3 || !strings.Contains(segs[0], ".") {
		return "", invalid("repo remote must look like host/owner/name (got %q)", s)
	}
	for _, seg := range segs {
		if seg == "." || seg == ".." || !repoSegRE.MatchString(seg) {
			return "", invalid("repo remote has an invalid path segment")
		}
	}
	return r, nil
}

// CanonicalRepo returns the key stored for a task repo: remote URLs (anything
// containing '/' or ':') are normalized with NormalizeRepo, legacy logical
// keys (ValidRepo) pass through. Task repos, session repos and daemon claim
// lists are compared in this form.
func CanonicalRepo(s string) (string, error) {
	if strings.ContainsAny(s, "/:") {
		return NormalizeRepo(s)
	}
	if !ValidRepo(s) {
		return "", invalid("repo must be a logical key matching %s or a git remote URL", repoRE)
	}
	return s, nil
}
