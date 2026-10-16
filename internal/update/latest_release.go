package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
)

// errNotFound is an HTTP 404 from the release server.
var errNotFound = errors.New("not found")

// noRelease explains a 404 on the latest-release URL.
func noRelease(base string) error {
	return fmt.Errorf("no release published yet at %s; install with `%s` or ask the maintainer to publish a release", base, goInstallHint(""))
}

// latestRedirectRE matches where GitHub's latest/download URL redirects.
var latestRedirectRE = regexp.MustCompile(`/releases/download/([^/]+)/SHA256SUMS$`)

// latestTag learns the latest release tag without downloading it: GitHub
// answers .../releases/latest/download/SHA256SUMS with a redirect to
// .../releases/download/<tag>/SHA256SUMS, which is read but not followed.
// "" means the base gave no usable redirect (a mirror, file://): the caller
// downloads the release to read its version instead.
func (o *Options) latestTag(ctx context.Context) (string, error) {
	base := o.releaseBase("")
	u, err := url.Parse(base + "/SHA256SUMS")
	if err != nil || u.Scheme == "file" || !o.schemeAllowed(u.Scheme) {
		return "", nil
	}
	client := *o.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	if err != nil {
		return "", nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil // the download path reports the failure
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", noRelease(base)
	}
	loc, err := resp.Location()
	if resp.StatusCode < 300 || resp.StatusCode > 399 || err != nil {
		return "", nil
	}
	if m := latestRedirectRE.FindStringSubmatch(loc.Path); m != nil && releaseTagRE.MatchString(m[1]) {
		return m[1], nil
	}
	return "", nil
}
