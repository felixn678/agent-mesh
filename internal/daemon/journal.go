package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Publish steps, in order. The journal is fsynced with the next step before
// that step's side effect runs, so a crash at any point resumes safely.
const (
	stepPrepare    = "prepare"     // scans, leftover commit
	stepPushing    = "pushing"     // push may or may not have happened
	stepPushed     = "pushed"      // branch is on origin
	stepPRCreating = "pr_creating" // PR may or may not exist: look up by head first
	stepPRCreated  = "pr_created"  // pr_url known (or empty on gh failure); completing
	stepClosingPR  = "closing_pr"  // lease lost after PR: close it, retried until ok
)

// journal is one publish in flight: spool/task-<id>-f<fence>.json.
type journal struct {
	ID          int64   `json:"id"`
	Fence       int64   `json:"fence"`
	Repo        string  `json:"repo"`
	Base        string  `json:"base"`
	Branch      string  `json:"branch"`
	Worktree    string  `json:"worktree"`
	SessionID   string  `json:"session_id"`
	CostUSD     float64 `json:"cost_usd"`
	Step        string  `json:"step"`
	PRURL       string  `json:"pr_url,omitempty"`
	PublishNote string  `json:"publish_note,omitempty"`
	Summary     string  `json:"summary,omitempty"` // claude's result text, for the creator session only
}

// writePrivate writes a 0600 file atomically and durably (tmp, fsync,
// rename, fsync dir).
func writePrivate(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func writeJSONFile(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writePrivate(path, b)
}

func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (d *Daemon) saveJournal(j *journal, step string) error {
	j.Step = step
	return writeJSONFile(d.names(j.ID, j.Fence).journal, j)
}

// loadJournals returns every pending publish (corrupt files are skipped and
// left for a human).
func (d *Daemon) loadJournals() []*journal {
	ents, _ := os.ReadDir(d.dir("spool"))
	var out []*journal
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var j journal
		if err := readJSONFile(filepath.Join(d.dir("spool"), e.Name()), &j); err != nil || j.ID < 1 {
			d.log.Error("unreadable journal left in place", "file", e.Name(), "err", err)
			continue
		}
		out = append(out, &j)
	}
	return out
}
