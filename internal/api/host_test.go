package api

import "testing"

func TestHostSummary(t *testing.T) {
	cases := []struct {
		h    Host
		want string
	}{
		{Host{Name: "box", HostLoad: HostLoad{Cores: 8, RAMGB: 8, Slots: 1, ThrottleReason: "cpu 85%"}},
			"box: 8c/8GB → 1 slot, throttled: cpu 85%"},
		{Host{Name: "big", HostLoad: HostLoad{Cores: 16, RAMGB: 32, Slots: 4}},
			"big: 16c/32GB → 4 slots"},
		{Host{Name: "pinned", HostLoad: HostLoad{Cores: 12, RAMGB: 36, Slots: 1, SlotsOverridden: true}},
			"pinned: 12c/36GB → 1 slot (override)"},
		{Host{Name: "new"}, "new: hardware unknown"},
	}
	for _, c := range cases {
		if got := c.h.Summary(); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}
