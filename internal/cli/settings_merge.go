package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func hookCommand(bin, event string) string { return shellQuote(bin) + " hook " + event }

// hookEntry is the matcher group appended to an event array (no matcher:
// applies to every session source).
func hookEntry(bin, event string) string {
	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string][]hook{"hooks": {{Type: "command", Command: hookCommand(bin, event), Timeout: hookTimeoutSecs}}})
	return strings.TrimSpace(b.String())
}

// ownsGroup reports whether a matcher group is one of ours: exactly one hook
// whose command is this binary's hook command, or any older install path
// ending in "agent-mesh hook <event>".
func ownsGroup(src []byte, g *jnode, bin, event string) bool {
	if g.kind != '{' {
		return false
	}
	hooks, _ := g.member("hooks")
	if hooks == nil || hooks.val.kind != '[' || len(hooks.val.elems) != 1 {
		return false
	}
	var h struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(src[hooks.val.elems[0].start:hooks.val.elems[0].end], &h) != nil {
		return false
	}
	unquoted := strings.NewReplacer("'", "", `"`, "").Replace(h.Command)
	return h.Command == hookCommand(bin, event) || strings.HasSuffix(unquoted, "agent-mesh hook "+event)
}

// Merge adds our hook groups to settings.json bytes, appending one element
// per missing event and leaving every other byte as it was. It applies one
// insertion at a time and re-parses, which keeps the splicing trivial.
func Merge(in []byte, bin string) ([]byte, bool, error) {
	if len(bytes.TrimSpace(in)) == 0 {
		in = []byte("{}\n")
	}
	out, changed := in, false
	for {
		next, did, err := mergeStep(out, bin)
		if err != nil {
			return in, false, err
		}
		if !did {
			return out, changed, nil
		}
		out, changed = next, true
	}
}

func mergeStep(src []byte, bin string) ([]byte, bool, error) {
	root, err := parseSpans(src)
	if err != nil {
		return nil, false, err
	}
	if root.kind != '{' {
		return nil, false, fmt.Errorf("settings root must be an object")
	}
	hooks, _ := root.member("hooks")
	if hooks == nil {
		return appendMember(src, root, `"hooks": {}`), true, nil
	}
	if hooks.val.kind != '{' {
		return nil, false, fmt.Errorf(`settings "hooks" must be an object`)
	}
	for _, ev := range hookEvents {
		m, _ := hooks.val.member(ev.claude)
		if m == nil {
			return appendMember(src, hooks.val, fmt.Sprintf("%q: [%s]", ev.claude, hookEntry(bin, ev.ours))), true, nil
		}
		if m.val.kind != '[' {
			return nil, false, fmt.Errorf("settings hooks.%s must be an array", ev.claude)
		}
		present := false
		for _, g := range m.val.elems {
			present = present || ownsGroup(src, g, bin, ev.ours)
		}
		if !present {
			return appendElem(src, m.val, hookEntry(bin, ev.ours)), true, nil
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

// Unmerge removes our hook groups. An event array (or the hooks object)
// emptied by the removal is removed as well, so a file Merge changed returns
// to its original bytes.
func Unmerge(in []byte, bin string) ([]byte, bool, error) {
	out, changed := in, false
	for {
		next, did, err := unmergeStep(out, bin)
		if err != nil {
			return in, false, err
		}
		if !did {
			return out, changed, nil
		}
		out, changed = next, true
	}
}

func unmergeStep(src []byte, bin string) ([]byte, bool, error) {
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
			if !ownsGroup(src, g, bin, ev.ours) {
				continue
			}
			switch {
			case len(m.val.elems) > 1:
				return removeElem(src, m.val.elems, gi), true, nil
			case len(hooks.val.members) > 1:
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
func removeElem(src []byte, elems []*jnode, i int) []byte {
	if i > 0 {
		return splice(src, elems[i-1].end, elems[i].end, "")
	}
	return splice(src, elems[0].start, elems[1].start, "")
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
