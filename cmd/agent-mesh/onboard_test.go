package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestStripNoClaude(t *testing.T) {
	for _, tc := range []struct {
		in      []string
		want    []string
		connect bool
	}{
		{[]string{"join"}, []string{"join"}, true},
		{[]string{"up", "--no-claude", "--daemon"}, []string{"up", "--daemon"}, false},
		{[]string{"invite", "laptop"}, []string{"invite", "laptop"}, false},
		{[]string{"doctor"}, []string{"doctor"}, false},
	} {
		got, connect := stripNoClaude(tc.in)
		if !reflect.DeepEqual(got, tc.want) || connect != tc.connect {
			t.Errorf("stripNoClaude(%v) = %v, %v", tc.in, got, connect)
		}
	}
}

func TestConnectClaudeReportsInsteadOfFailing(t *testing.T) {
	origOn, origSetup := claudeOnPath, setupClaude
	t.Cleanup(func() { claudeOnPath, setupClaude = origOn, origSetup })
	ran := 0
	setupClaude = func(context.Context) error { ran++; return nil }

	var out bytes.Buffer
	claudeOnPath = func() bool { return false }
	connectClaude(context.Background(), &out)
	if ran != 0 || !strings.Contains(out.String(), "not on PATH") {
		t.Errorf("no claude: ran=%d out=%s", ran, out.String())
	}

	out.Reset()
	claudeOnPath = func() bool { return true }
	connectClaude(context.Background(), &out)
	if ran != 1 || !strings.Contains(out.String(), "All set") {
		t.Errorf("claude: ran=%d out=%s", ran, out.String())
	}

	out.Reset()
	setupClaude = func(context.Context) error { return errors.New("boom") }
	connectClaude(context.Background(), &out)
	if !strings.Contains(out.String(), "not connected: boom") {
		t.Errorf("failure: %s", out.String())
	}
}
