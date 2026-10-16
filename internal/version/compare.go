package version

import (
	"strconv"
	"strings"
)

// Header carries the hub's version on every hub response, so clients notice
// a version difference without an extra call.
const Header = "X-AgentMesh-Version"

// Compare orders two release versions "vX.Y.Z" (the "v" is optional) and
// returns -1, 0 or +1. ok is false when either is not a plain release: "dev",
// pseudo-versions, pre-releases and empty strings (an older peer that never
// reported its version) compare as unknown, so callers never warn on them.
func Compare(a, b string) (cmp int, ok bool) {
	x, okA := parse(a)
	y, okB := parse(b)
	if !okA || !okB {
		return 0, false
	}
	for i := range x {
		switch {
		case x[i] < y[i]:
			return -1, true
		case x[i] > y[i]:
			return 1, true
		}
	}
	return 0, true
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || p[0] == '+' || (len(p) > 1 && p[0] == '0') {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Skew describes how v differs from the hub's version: "" when they match or
// either is unknown, else a short note naming the fix, e.g.
// "v0.1.0 ≠ hub v0.1.2 → run `agent-mesh update` there".
func Skew(v, hub string) string {
	c, ok := Compare(v, hub)
	if !ok || c == 0 {
		return ""
	}
	fix := "there"
	if c > 0 {
		fix = "on the hub machine"
	}
	return v + " ≠ hub " + hub + " → run `agent-mesh update` " + fix
}

// LocalSkew is Skew from this binary's point of view: "" when it matches the
// hub or either version is unknown, else e.g. "this machine runs agent-mesh
// v0.1.0 but the hub runs v0.1.2 → run `agent-mesh update` here".
func LocalSkew(local, hub string) string {
	c, ok := Compare(local, hub)
	if !ok || c == 0 {
		return ""
	}
	fix := "here"
	if c > 0 {
		fix = "on the hub machine"
	}
	return "this machine runs agent-mesh " + local + " but the hub runs " + hub + " → run `agent-mesh update` " + fix
}
