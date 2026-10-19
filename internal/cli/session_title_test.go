package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const titleSession = "8f14e45f-ceea-4672-9a1b-2c3d4e5f6a7b"

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTranscriptTitleTakesTheLatestRenameOfThisSession(t *testing.T) {
	p := writeTranscript(t,
		`{"type":"custom-title","customTitle":"Old name","sessionId":"`+titleSession+`"}`,
		`{"type":"user","message":{"content":"mentions \"type\":\"custom-title\" in text"}}`,
		`{"type":"custom-title","customTitle":"  AgentMesh Mac Test ","sessionId":"`+titleSession+`"}`,
		`{"type":"custom-title","customTitle":"Other session","sessionId":"aaaa-1111"}`,
		`{"type":"assistant","message":{}}`)
	if got := transcriptTitle(p, titleSession); got != "AgentMesh Mac Test" {
		t.Fatalf("title = %q", got)
	}
}

func TestTranscriptTitleFallsBackToTheWholeFile(t *testing.T) {
	filler := `{"type":"assistant","pad":"` + strings.Repeat("x", 4096) + `"}`
	lines := []string{`{"type":"custom-title","customTitle":"Early","sessionId":"` + titleSession + `"}`}
	for range titleTailBytes/len(filler) + 2 {
		lines = append(lines, filler)
	}
	if got := transcriptTitle(writeTranscript(t, lines...), titleSession); got != "Early" {
		t.Fatalf("title = %q", got)
	}
}

func TestTranscriptTitleWithoutRenameOrFile(t *testing.T) {
	if got := transcriptTitle(writeTranscript(t, `{"type":"user"}`), titleSession); got != "" {
		t.Fatalf("title = %q", got)
	}
	if got := transcriptTitle(filepath.Join(t.TempDir(), "missing.jsonl"), titleSession); got != "" {
		t.Fatalf("title = %q", got)
	}
}

func TestSessionNameEnvWinsOverRename(t *testing.T) {
	p := writeTranscript(t, `{"type":"custom-title","customTitle":"Renamed","sessionId":"`+titleSession+`"}`)
	in := hookInput{SessionID: titleSession, TranscriptPath: p}
	t.Setenv("AGENTMESH_SESSION_NAME", "")
	if got := sessionName(in); got != "Renamed" {
		t.Fatalf("name = %q", got)
	}
	t.Setenv("AGENTMESH_SESSION_NAME", "frontend")
	if got := sessionName(in); got != "frontend" {
		t.Fatalf("name = %q", got)
	}
}

func TestTruncateNameKeepsWholeRunes(t *testing.T) {
	got := truncateName(strings.Repeat("ữ", 50)) // 3 bytes each
	if len(got) > 100 || !strings.HasPrefix(strings.Repeat("ữ", 50), got) || len(got)%3 != 0 {
		t.Fatalf("truncated to %d bytes: %q", len(got), got)
	}
}
