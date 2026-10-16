package cli

import (
	"fmt"
	"slices"
)

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
