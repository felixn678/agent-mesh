package onboard

import (
	"fmt"
	"strings"
)

// Minimal line-based TOML edits. hub.toml holds user comments, so we splice
// lines instead of re-encoding the document; callers re-validate the result
// with the real loader before it replaces the file.

func isHeader(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "[") }

func isEntry(line string) bool {
	t := strings.TrimSpace(line)
	return t != "" && !strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "[")
}

// addToTable appends entry (a full `key = value` line) to [table], after the
// table's last existing entry. A missing table is created at the end.
func addToTable(src, table, entry string) string {
	lines := strings.Split(strings.TrimRight(src, "\n"), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "["+table+"]") {
			start = i
			break
		}
	}
	if start < 0 {
		return strings.Join(lines, "\n") + fmt.Sprintf("\n\n[%s]\n%s\n", table, entry)
	}
	at := start // insert after the last entry in the table's range
	for i := start + 1; i < len(lines) && !isHeader(lines[i]); i++ {
		if isEntry(lines[i]) {
			at = i
		}
	}
	out := append([]string{}, lines[:at+1]...)
	out = append(out, entry)
	out = append(out, lines[at+1:]...)
	return strings.Join(out, "\n") + "\n"
}

// removeFromTable drops the entries of [table] whose key is key (bare or
// quoted), keeping comments and every other line.
func removeFromTable(src, table, key string) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	in := false
	for _, l := range lines {
		if isHeader(l) {
			in = strings.HasPrefix(strings.TrimSpace(l), "["+table+"]")
		} else if in && isEntry(l) {
			k, _, _ := strings.Cut(strings.TrimSpace(l), "=")
			if strings.Trim(strings.TrimSpace(k), `"`) == key {
				continue
			}
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// setTopLevel rewrites `key = value` among the top-level keys (before the
// first table header), keeping any trailing comment. It reports whether the
// key was found.
func setTopLevel(src, key, value string) (string, bool) {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if isHeader(l) {
			break
		}
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, key) || !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(t, key)), "=") {
			continue
		}
		comment := ""
		if j := strings.Index(l, "#"); j >= 0 && !strings.Contains(l[:j], `"#`) {
			comment = "   " + l[j:]
		}
		lines[i] = fmt.Sprintf("%s = %q%s", key, value, comment)
		return strings.Join(lines, "\n"), true
	}
	return src, false
}
