package version

import (
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		cmp  int
		ok   bool
	}{
		{"v0.1.0", "v0.1.2", -1, true},
		{"v1.0.0", "v0.9.9", 1, true},
		{"v0.10.0", "v0.9.0", 1, true},
		{"0.1.2", "v0.1.2", 0, true},
		{"dev", "v0.1.2", 0, false},
		{"v0.1.2", "", 0, false},
		{"v0.0.0-20261016123456-abcdef123456", "v0.1.0", 0, false},
		{"v0.1.0-rc.1", "v0.1.0", 0, false},
		{"v0.1.0+dirty", "v0.1.0", 0, false},
		{"v1.2", "v1.2.0", 0, false},
		{"v01.2.3", "v1.2.3", 0, false},
		{"v1.+2.3", "v1.2.3", 0, false},
	} {
		cmp, ok := Compare(c.a, c.b)
		if cmp != c.cmp || ok != c.ok {
			t.Errorf("Compare(%q, %q) = %d, %v; want %d, %v", c.a, c.b, cmp, ok, c.cmp, c.ok)
		}
	}
}

func TestSkew(t *testing.T) {
	if got := Skew("v0.1.0", "v0.1.2"); got != "v0.1.0 ≠ hub v0.1.2 → run `agent-mesh update` there" {
		t.Errorf("older: %q", got)
	}
	if got := Skew("v0.2.0", "v0.1.2"); got != "v0.2.0 ≠ hub v0.1.2 → run `agent-mesh update` on the hub machine" {
		t.Errorf("newer: %q", got)
	}
	for _, p := range [][2]string{{"v0.1.2", "v0.1.2"}, {"dev", "v0.1.2"}, {"v0.1.2", ""}, {"", ""}} {
		if got := Skew(p[0], p[1]); got != "" {
			t.Errorf("Skew(%q, %q) = %q, want none", p[0], p[1], got)
		}
	}
}

func TestLocalSkew(t *testing.T) {
	if got := LocalSkew("v0.1.0", "v0.1.2"); got != "this machine runs agent-mesh v0.1.0 but the hub runs v0.1.2 → run `agent-mesh update` here" {
		t.Errorf("older: %q", got)
	}
	if got := LocalSkew("v0.1.3", "v0.1.2"); !strings.HasSuffix(got, "on the hub machine") {
		t.Errorf("newer: %q", got)
	}
	if LocalSkew("v0.1.2", "v0.1.2") != "" || LocalSkew("dev", "v0.1.2") != "" || LocalSkew("v0.1.2", "") != "" {
		t.Error("unexpected note for equal or unknown versions")
	}
}
