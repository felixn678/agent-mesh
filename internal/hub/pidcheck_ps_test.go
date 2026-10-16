package hub

import (
	"reflect"
	"testing"
)

func TestSplitPSCommand(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		hub  bool
	}{
		{"/Users/x/.local/bin/agent-mesh hub\n", []string{"/Users/x/.local/bin/agent-mesh", "hub"}, true},
		{"/Users/x/Library/Application Support/tools/agent-mesh hub -config /a b\n",
			[]string{"/Users/x/Library/Application Support/tools/agent-mesh", "hub", "-config", "/a", "b"}, true},
		{"/Users/x/.local/bin/agent-mesh daemon", []string{"/Users/x/.local/bin/agent-mesh", "daemon"}, false},
		{"/bin/zsh -l", []string{"/bin/zsh", "-l"}, false},
		{"", nil, false},
	}
	for _, c := range cases {
		got := splitPSCommand(c.in)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitPSCommand(%q) = %q, want %q", c.in, got, c.want)
		}
		if isHubArgs(got) != c.hub {
			t.Errorf("isHubArgs(%q) = %v, want %v", got, !c.hub, c.hub)
		}
	}
}
