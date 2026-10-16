package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

const testBin = "/opt/agent mesh/bin/agent-mesh"

func TestMergeKeepsClaudeKitBytesAndUnmergeRestoresThem(t *testing.T) {
	orig := fixture(t, "settings_claudekit.json")
	merged, changed, err := Merge([]byte(orig), testBin)
	if err != nil || !changed {
		t.Fatalf("merge: %v %v", changed, err)
	}
	var parsed struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(merged, &parsed); err != nil {
		t.Fatalf("merged JSON invalid: %v\n%s", err, merged)
	}
	for _, ev := range hookEvents {
		groups := parsed.Hooks[ev.claude]
		last := groups[len(groups)-1]
		if last.Matcher != "" || len(last.Hooks) != 1 || last.Hooks[0].Command != "'/opt/agent mesh/bin/agent-mesh' hook "+ev.ours || last.Hooks[0].Timeout != 3 {
			t.Fatalf("%s: %+v", ev.claude, groups)
		}
	}
	if len(parsed.Hooks["SessionStart"]) != 2 || len(parsed.Hooks["PreToolUse"]) != 1 {
		t.Fatalf("existing groups disturbed: %+v", parsed.Hooks)
	}
	// Only insertions: deleting every added byte span must give the original
	// back, i.e. the original is a subsequence with untouched key order.
	if !isSubsequence(orig, string(merged)) {
		t.Fatalf("original bytes not preserved:\n%s", merged)
	}
	if strings.Index(string(merged), `"statusLine"`) > strings.Index(string(merged), `"hooks"`) {
		t.Fatal("key order changed")
	}
	again, changed, err := Merge(merged, testBin)
	if err != nil || changed || string(again) != string(merged) {
		t.Fatalf("second merge changed=%v err=%v", changed, err)
	}
	back, changed, err := Unmerge(merged, testBin)
	if err != nil || !changed || string(back) != orig {
		t.Fatalf("unmerge not byte-identical (changed=%v err=%v):\n%s", changed, err, back)
	}
	if _, changed, _ := Unmerge([]byte(orig), testBin); changed {
		t.Fatal("unmerge of a clean file must be a no-op")
	}
}

func TestMergeEdgeCases(t *testing.T) {
	out, changed, err := Merge(nil, testBin)
	if err != nil || !changed || !json.Valid(out) || strings.Count(string(out), " hook ") != 4 {
		t.Fatalf("missing file: %s %v", out, err)
	}
	for _, in := range []string{`{"hooks": `, `[]`, `{"hooks": []}`, `{"hooks": {"Stop": {}}}`} {
		if _, _, err := Merge([]byte(in), testBin); err == nil {
			t.Fatalf("Merge(%s) must fail", in)
		}
	}
	// Compact file with no hooks key; older install path counts as ours.
	in := `{"model":"opus"}`
	out, _, _ = Merge([]byte(in), testBin)
	back, _, _ := Unmerge(out, "/other/path/agent-mesh")
	if string(back) != in {
		t.Fatalf("compact round trip: %s", back)
	}
	// A user hook added after ours survives uninstall.
	out, _, _ = Merge([]byte(`{"hooks":{"Stop":[]}}`), testBin)
	out = []byte(strings.Replace(string(out), `"timeout":3}]}]`, `"timeout":3}]},{"hooks":[{"type":"command","command":"say done"}]}]`, 1))
	back, _, err = Unmerge(out, testBin)
	if err != nil || !strings.Contains(string(back), "say done") || strings.Contains(string(back), "agent-mesh") || !json.Valid(back) {
		t.Fatalf("user hook after ours: %s %v", back, err)
	}
	// Known limitation: an originally empty event array is dropped with our entry.
	out, _, _ = Merge([]byte(`{"hooks":{"Stop":[],"Other":[]}}`), testBin)
	back, _, _ = Unmerge(out, testBin)
	if string(back) != `{"hooks":{"Other":[]}}` {
		t.Fatalf("empty array: %s", back)
	}
}

func isSubsequence(sub, s string) bool {
	i := 0
	for j := 0; i < len(sub) && j < len(s); j++ {
		if sub[i] == s[j] {
			i++
		}
	}
	return i == len(sub)
}

func TestLineDiff(t *testing.T) {
	got := lineDiff("a\nb\nc\n", "a\nx\nc\n")
	if got != " a\n-b\n+x\n c\n" {
		t.Fatalf("%q", got)
	}
}
