package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// hookEvents maps Claude hook events to `agent-mesh hook <event>` names.
var hookEvents = []struct{ claude, ours string }{
	{"SessionStart", "session-start"},
	{"UserPromptSubmit", "prompt"},
	{"Stop", "stop"},
	{"SessionEnd", "session-end"},
}

// hookTimeoutSecs is Claude's per-hook timeout (seconds), a backstop to the
// hook's own 800 ms budget.
const hookTimeoutSecs = 3

// managedFlag marks every command setup-claude writes. Ownership is the
// exact word list `<path>/agent-mesh hook <event> --managed-by=agent-mesh`,
// so a user command that merely mentions agent-mesh is never touched.
const managedFlag = "--managed-by=agent-mesh"

func hookCommand(bin, event string) string {
	return shellQuote(bin) + " hook " + event + " " + managedFlag
}

// managedEvent returns the event of a command setup-claude wrote.
func managedEvent(command string) (string, bool) {
	w, ok := shellWords(command)
	if !ok || len(w) != 4 || filepath.Base(w[0]) != "agent-mesh" || w[1] != "hook" || w[3] != managedFlag {
		return "", false
	}
	return w[2], true
}

// hookEntry is the matcher group appended to an event array (no matcher:
// applies to every session source).
func hookEntry(bin, event string) string {
	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	return compactJSON(map[string][]hook{"hooks": {{Type: "command", Command: hookCommand(bin, event), Timeout: hookTimeoutSecs}}})
}

func compactJSON(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSpace(b.String())
}

// managedCommand returns the "command" value node of a matcher group that is
// exactly one managed hook for event, or nil.
func managedCommand(src []byte, g *jnode, event string) *jnode {
	if g.kind != '{' {
		return nil
	}
	hooks, _ := g.member("hooks")
	if hooks == nil || hooks.val.kind != '[' || len(hooks.val.elems) != 1 || hooks.val.elems[0].kind != '{' {
		return nil
	}
	cmd, _ := hooks.val.elems[0].member("command")
	if cmd == nil || cmd.val.kind != '"' {
		return nil
	}
	var s string
	if json.Unmarshal(src[cmd.val.start:cmd.val.end], &s) != nil {
		return nil
	}
	if ev, ok := managedEvent(s); !ok || ev != event {
		return nil
	}
	return cmd.val
}

// Created records which containers Merge added, so Unmerge removes exactly
// those and leaves a user's own empty "hooks": {} or "Stop": [] alone.
type Created struct {
	Hooks  bool     `json:"hooks,omitempty"`
	Events []string `json:"events,omitempty"`
}

// Union merges two records (a re-run creates nothing new).
func (c Created) Union(o Created) Created {
	out := Created{Hooks: c.Hooks || o.Hooks, Events: slices.Clone(c.Events)}
	for _, e := range o.Events {
		if !slices.Contains(out.Events, e) {
			out.Events = append(out.Events, e)
		}
	}
	return out
}

// Merge adds our hook groups to settings.json bytes, appending one element
// per missing event and rewriting the command of an existing managed entry
// whose binary path changed. Every other byte stays as it was. It applies one
// edit at a time and re-parses, which keeps the splicing trivial.
func Merge(in []byte, bin string) ([]byte, bool, Created, error) {
	if len(bytes.TrimSpace(in)) == 0 {
		in = []byte("{}\n")
	}
	out, changed, created := in, false, Created{}
	for {
		next, did, err := mergeStep(out, bin, &created)
		if err != nil {
			return in, false, Created{}, err
		}
		if !did {
			return out, changed, created, nil
		}
		out, changed = next, true
	}
}

func mergeStep(src []byte, bin string, created *Created) ([]byte, bool, error) {
	root, err := parseSpans(src)
	if err != nil {
		return nil, false, err
	}
	if root.kind != '{' {
		return nil, false, fmt.Errorf("settings root must be an object")
	}
	hooks, _ := root.member("hooks")
	if hooks == nil {
		created.Hooks = true
		return appendMember(src, root, `"hooks": {}`), true, nil
	}
	if hooks.val.kind != '{' {
		return nil, false, fmt.Errorf(`settings "hooks" must be an object`)
	}
	for _, ev := range hookEvents {
		m, _ := hooks.val.member(ev.claude)
		if m == nil {
			created.Events = append(created.Events, ev.claude)
			return appendMember(src, hooks.val, fmt.Sprintf("%q: [%s]", ev.claude, hookEntry(bin, ev.ours))), true, nil
		}
		if m.val.kind != '[' {
			return nil, false, fmt.Errorf("settings hooks.%s must be an array", ev.claude)
		}
		var mine *jnode
		for _, g := range m.val.elems {
			if c := managedCommand(src, g, ev.ours); c != nil {
				mine = c
				break
			}
		}
		want := compactJSON(hookCommand(bin, ev.ours))
		switch {
		case mine == nil:
			return appendElem(src, m.val, hookEntry(bin, ev.ours)), true, nil
		case string(src[mine.start:mine.end]) != want:
			return splice(src, mine.start, mine.end, want), true, nil // binary moved: repair in place
		}
	}
	return nil, false, nil
}

// appendMember inserts `text` as the last member of object n.
func appendMember(src []byte, n *jnode, text string) []byte {
	if len(n.members) == 0 {
		return splice(src, n.start+1, n.start+1, text)
	}
	last := n.members[len(n.members)-1]
	return splice(src, last.val.end, last.val.end, ","+sepBefore(src, last.keyStart)+text)
}

// appendElem inserts `text` as the last element of array n.
func appendElem(src []byte, n *jnode, text string) []byte {
	if len(n.elems) == 0 {
		return splice(src, n.start+1, n.start+1, text)
	}
	last := n.elems[len(n.elems)-1]
	return splice(src, last.end, last.end, ","+sepBefore(src, last.start)+text)
}

// Unmerge removes every managed hook group. An event array or the hooks
// object is removed only when it is emptied and created says Merge added it,
// so a file Merge changed returns to its original bytes.
func Unmerge(in []byte, created Created) ([]byte, bool, error) {
	out, changed := in, false
	for {
		next, did, err := unmergeStep(out, created)
		if err != nil {
			return in, false, err
		}
		if !did {
			return out, changed, nil
		}
		out, changed = next, true
	}
}

func unmergeStep(src []byte, created Created) ([]byte, bool, error) {
	root, err := parseSpans(src)
	if err != nil {
		return nil, false, err
	}
	if root.kind != '{' {
		return nil, false, fmt.Errorf("settings root must be an object")
	}
	hooks, hooksIdx := root.member("hooks")
	if hooks == nil || hooks.val.kind != '{' {
		return nil, false, nil
	}
	for _, ev := range hookEvents {
		m, mIdx := hooks.val.member(ev.claude)
		if m == nil || m.val.kind != '[' {
			continue
		}
		for gi, g := range m.val.elems {
			if managedCommand(src, g, ev.ours) == nil {
				continue
			}
			ownKey := len(m.val.elems) == 1 && slices.Contains(created.Events, ev.claude)
			switch {
			case !ownKey:
				return removeElem(src, m.val, gi), true, nil
			case len(hooks.val.members) > 1 || !created.Hooks:
				return removeMember(src, hooks.val.members, mIdx), true, nil
			default:
				return removeMember(src, root.members, hooksIdx), true, nil
			}
		}
	}
	return nil, false, nil
}

// removeElem deletes element i together with the separator that Merge added
// before it (or the one after it when it is first).
func removeElem(src []byte, arr *jnode, i int) []byte {
	elems := arr.elems // non-empty: i indexes it
	switch {
	case i > 0:
		return splice(src, elems[i-1].end, elems[i].end, "")
	case len(elems) > 1:
		return splice(src, elems[0].start, elems[1].start, "")
	}
	return splice(src, elems[0].start, elems[0].end, "") // leave the user's own empty array
}

func removeMember(src []byte, ms []jmember, i int) []byte {
	switch {
	case i > 0:
		return splice(src, ms[i-1].val.end, ms[i].val.end, "")
	case len(ms) > 1:
		return splice(src, ms[0].keyStart, ms[1].keyStart, "")
	}
	return splice(src, ms[0].keyStart, ms[0].val.end, "")
}
