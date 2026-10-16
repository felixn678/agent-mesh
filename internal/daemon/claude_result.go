package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// Result is the final line of a `claude -p --output-format stream-json` run.
type Result struct {
	Found     bool // a type:"result" line was seen
	Text      string
	SessionID string
	CostUSD   float64
	IsError   bool
	Subtype   string
}

type streamLine struct {
	Type      string  `json:"type"`
	Subtype   string  `json:"subtype"`
	SessionID string  `json:"session_id"`
	Result    string  `json:"result"`
	CostUSD   float64 `json:"total_cost_usd"`
	IsError   bool    `json:"is_error"`
}

// parseResult scans stream-json lines; the session id comes from the init
// line (or the result line), the outcome from the last result line.
// Unparseable lines are skipped: a crash can leave a partial last line.
func parseResult(r io.Reader) (Result, error) {
	var res Result
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var l streamLine
			if json.Unmarshal(line, &l) == nil {
				if l.SessionID != "" && res.SessionID == "" {
					res.SessionID = l.SessionID
				}
				if l.Type == "result" {
					res = Result{Found: true, Text: l.Result, SessionID: firstNonEmpty(l.SessionID, res.SessionID),
						CostUSD: l.CostUSD, IsError: l.IsError, Subtype: l.Subtype}
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return res, nil
		}
		if err != nil {
			return res, err
		}
	}
}

func parseResultFile(path string) (Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	return parseResult(f)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
