package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// titleTailBytes is how much of the transcript's end is searched first for
// the session title. Claude Code re-appends its title record every few dozen
// lines, so the tail usually holds the current one.
const titleTailBytes = 1 << 20

// maxSessionName matches the hub's limit on session names.
const maxSessionName = 100

// sessionName is the name other sessions address this one by:
// $AGENTMESH_SESSION_NAME, else the title set with /rename in Claude Code.
func sessionName(in hookInput) string {
	if n := strings.TrimSpace(os.Getenv("AGENTMESH_SESSION_NAME")); n != "" {
		return truncateName(n)
	}
	return truncateName(transcriptTitle(in.TranscriptPath, in.SessionID))
}

// transcriptTitle returns the last /rename title recorded for session in the
// transcript: from its tail, else from the whole file ("" when there is none
// or the file cannot be read).
func transcriptTitle(path, session string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	if st.Size() > titleTailBytes {
		if _, err := f.Seek(-titleTailBytes, io.SeekEnd); err == nil {
			if data, err := io.ReadAll(f); err == nil {
				if title, ok := lastTitle(data, session); ok {
					return title
				}
			}
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	title, _ := lastTitle(data, session)
	return title
}

// lastTitle finds the last custom-title record of session in JSONL data.
func lastTitle(data []byte, session string) (string, bool) {
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if !bytes.Contains(lines[i], []byte(`"custom-title"`)) {
			continue
		}
		var rec struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
			SessionID   string `json:"sessionId"`
		}
		if json.Unmarshal(lines[i], &rec) == nil && rec.Type == "custom-title" && rec.SessionID == session {
			return strings.TrimSpace(rec.CustomTitle), true
		}
	}
	return "", false
}

// truncateName cuts a name to the hub's limit on a rune boundary.
func truncateName(s string) string {
	for len(s) > maxSessionName {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}
