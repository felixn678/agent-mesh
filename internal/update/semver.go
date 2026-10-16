package update

import (
	"regexp"
	"strconv"
	"strings"
)

// releaseTagRE matches the release tags this tool installs: vMAJOR.MINOR.PATCH
// with an optional pre-release suffix. Anything else never reaches a URL.
var releaseTagRE = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// pseudoRE matches Go pseudo-versions (v0.0.0-20261016120000-abcdef123456,
// v1.2.4-0.20261016120000-abcdef123456), which a plain build in a checkout gets.
var pseudoRE = regexp.MustCompile(`\d{14}-[0-9a-f]{12}(\+dirty)?$`)

// normalizeTag accepts "1.2.3" or "v1.2.3" and returns the tag, or "" when
// it is not a release tag.
func normalizeTag(s string) string {
	if !strings.HasPrefix(s, "v") {
		s = "v" + s
	}
	if !releaseTagRE.MatchString(s) {
		return ""
	}
	return s
}

// isDevBuild reports whether v is not a release: "dev", a pseudo-version or
// anything else that is not a release tag.
func isDevBuild(v string) bool {
	return !releaseTagRE.MatchString(v) || pseudoRE.MatchString(v)
}

// compareTags orders two release tags like semver: -1, 0 or 1. A release
// sorts after its pre-releases; pre-release fields compare numerically when
// both are numbers.
func compareTags(a, b string) int {
	ma, mb := releaseTagRE.FindStringSubmatch(a), releaseTagRE.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		if c := compareNum(ma[i], mb[i]); c != 0 {
			return c
		}
	}
	pa, pb := ma[4], mb[4]
	switch {
	case pa == pb:
		return 0
	case pa == "":
		return 1
	case pb == "":
		return -1
	}
	fa, fb := strings.Split(pa, "."), strings.Split(pb, ".")
	for i := 0; i < len(fa) && i < len(fb); i++ {
		if c := compareField(fa[i], fb[i]); c != 0 {
			return c
		}
	}
	return compareNum(strconv.Itoa(len(fa)), strconv.Itoa(len(fb)))
}

func compareField(a, b string) int {
	_, ea := strconv.ParseUint(a, 10, 64)
	_, eb := strconv.ParseUint(b, 10, 64)
	switch {
	case ea == nil && eb == nil:
		return compareNum(a, b)
	case ea == nil:
		return -1 // numeric identifiers sort before alphanumeric ones
	case eb == nil:
		return 1
	}
	return strings.Compare(a, b)
}

// compareNum compares decimal strings without overflow.
func compareNum(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}
