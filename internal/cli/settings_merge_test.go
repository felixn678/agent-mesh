package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

const testBin = "/opt/agent mesh/bin/agent-mesh"

func TestMergeKeepsClaudeKitBytesAndUnmergeRestoresThem(t *testing.T) {
	orig := fixture(t, "settings_claudekit.json")
	merged, changed, created, err := Merge([]byte(orig), testBin)
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
		if last.Matcher != "" || len(last.Hooks) != 1 || last.Hooks[0].Timeout != 3 ||
			last.Hooks[0].Command != "'/opt/agent mesh/bin/agent-mesh' hook "+ev.ours+" --managed-by=agent-mesh" {
			t.Fatalf("%s: %+v", ev.claude, groups)
		}
	}
	if created.Hooks || strings.Join(created.Events, ",") != "Stop,SessionEnd" {
		t.Fatalf("created %+v", created)
	}
	if len(parsed.Hooks["SessionStart"]) != 2 || len(parsed.Hooks["PreToolUse"]) != 1 {
		t.Fatalf("existing groups disturbed: %+v", parsed.Hooks)
	}
	if !isSubsequence(orig, string(merged)) {
		t.Fatalf("original bytes not preserved:\n%s", merged)
	}
	again, changed, _, err := Merge(merged, testBin)
	if err != nil || changed || string(again) != string(merged) {
		t.Fatalf("second merge changed=%v err=%v", changed, err)
	}
	back, changed, err := Unmerge(merged, created)
	if err != nil || !changed || string(back) != orig {
		t.Fatalf("unmerge not byte-identical (changed=%v err=%v):\n%s", changed, err, back)
	}
	if _, changed, _ := Unmerge([]byte(orig), created); changed {
		t.Fatal("unmerge of a clean file must be a no-op")
	}
}

func TestMergeRepairsMovedBinaryInPlace(t *testing.T) {
	old := "/tmp/go-build123/exe/agent-mesh"
	first, _, created, _ := Merge([]byte(`{"model":"opus"}`), old)
	moved, changed, more, err := Merge(first, testBin)
	if err != nil || !changed || len(more.Events) != 0 || more.Hooks {
		t.Fatalf("repair: changed=%v created=%+v err=%v", changed, more, err)
	}
	if strings.Contains(string(moved), old) || strings.Count(string(moved), "'/opt/agent mesh/bin/agent-mesh' hook ") != 4 {
		t.Fatalf("paths not rewritten:\n%s", moved)
	}
	if len(moved)-len(first) != 4*(len("'"+testBin+"'")-len(old)) {
		t.Fatal("repair must only rewrite the command strings")
	}
	back, _, _ := Unmerge(moved, created)
	if string(back) != `{"model":"opus"}` {
		t.Fatalf("round trip: %s", back)
	}
}

func TestUserCommandsMentioningAgentMeshAreNotOurs(t *testing.T) {
	user := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"notify-send done; agent-mesh hook stop"}]},` +
		`{"hooks":[{"type":"command","command":"agent-mesh hook stop"}]},` +
		`{"hooks":[{"type":"command","command":"/usr/bin/agent-mesh hook stop --managed-by=agent-mesh; rm -rf ~"}]}]}}`
	merged, _, created, err := Merge([]byte(user), testBin)
	if err != nil || strings.Count(string(merged), "--managed-by=agent-mesh\"") != 4 {
		t.Fatalf("our Stop hook must still be added: %v\n%s", err, merged)
	}
	back, _, _ := Unmerge(merged, created)
	if string(back) != user {
		t.Fatalf("user hooks touched:\n%s", back)
	}
}

func TestUnmergeKeepsUserEmptyContainers(t *testing.T) {
	for _, in := range []string{`{"hooks":{}}`, `{"hooks":{"Stop":[],"Other":[]}}`, `{"hooks": { "Stop": [ ] }}`} {
		out, _, created, err := Merge([]byte(in), testBin)
		if err != nil {
			t.Fatal(err)
		}
		back, _, _ := Unmerge(out, created)
		if string(back) != in {
			t.Fatalf("%s -> %s", in, back)
		}
	}
	// Without a record (lost state file) uninstall still removes only ours.
	out, _, _, _ := Merge([]byte(`{}`), testBin)
	back, _, _ := Unmerge(out, Created{})
	if strings.Contains(string(back), "agent-mesh") || !json.Valid(back) {
		t.Fatalf("no record: %s", back)
	}
}

func TestMergeEdgeCases(t *testing.T) {
	out, changed, created, err := Merge(nil, testBin)
	if err != nil || !changed || !created.Hooks || !json.Valid(out) || strings.Count(string(out), " hook ") != 4 {
		t.Fatalf("missing file: %s %v", out, err)
	}
	for _, in := range []string{`{"hooks": `, `[]`, `{"hooks": []}`, `{"hooks": {"Stop": {}}}`} {
		if _, _, _, err := Merge([]byte(in), testBin); err == nil {
			t.Fatalf("Merge(%s) must fail", in)
		}
	}
}

func TestShellWords(t *testing.T) {
	for in, want := range map[string]string{
		`'/a b/agent-mesh' hook stop --managed-by=agent-mesh`: "/a b/agent-mesh|hook|stop|--managed-by=agent-mesh",
		`"/a\"b" x`: `/a"b|x`,
		`a\ b c`:    "a b|c",
	} {
		w, ok := shellWords(in)
		if !ok || strings.Join(w, "|") != want {
			t.Errorf("%q -> %q %v", in, w, ok)
		}
	}
	for _, in := range []string{`a; b`, `a | b`, `$(x)`, "`x`", `'open`, `a > f`, `"$HOME/x"`} {
		if _, ok := shellWords(in); ok {
			t.Errorf("%q accepted", in)
		}
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
