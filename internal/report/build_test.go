package report_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/report"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch (run with -update to accept)\n--- got ---\n%s", name, got)
	}
}

func TestOvernightReportViaHub(t *testing.T) {
	h := overnight(t)
	got, err := h.Admin().Report(bg, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "overnight.golden.md", got)
}

var now = time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)

func TestEmptyReportIsShort(t *testing.T) {
	got := report.Build(report.Input{Now: now, Since: now.Add(-16 * time.Hour)})
	if !strings.Contains(got, "Nothing ran") || strings.Contains(got, "|") || strings.Contains(got, "##") {
		t.Fatalf("empty report:\n%s", got)
	}
}

func TestStuckReasonEscapedToOneRow(t *testing.T) {
	task := api.Task{ID: 9, Repo: "web", State: api.StateStuck, ClaimedBy: "host-a", Attempts: 3, MaxAttempts: 3,
		Reason: "a | b\nc\r\nd " + strings.Repeat("x", 300), SessionID: "sess-9", Worktree: "/srv/wt/web 9"}
	got := report.Build(report.Input{Now: now, Since: now.Add(-time.Hour), Tasks: []api.Task{task}})
	var rows []string
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "| #9 ") {
			rows = append(rows, l)
		}
	}
	if len(rows) != 1 || !strings.Contains(rows[0], `a \| b c d x`) || !strings.Contains(rows[0], "x…") {
		t.Fatalf("rows %q\n%s", rows, got)
	}
	if !strings.Contains(got, "`cd '/srv/wt/web 9' && claude --resume sess-9`") {
		t.Fatalf("resume hint missing:\n%s", got)
	}
}

func TestDoneTableCapped(t *testing.T) {
	var tasks []api.Task
	for i := 1; i <= 52; i++ {
		tasks = append(tasks, api.Task{ID: int64(i), Repo: "web", State: api.StateDone, ClaimedBy: "host-a"})
	}
	got := report.Build(report.Input{Now: now, Since: now.Add(-time.Hour), Tasks: tasks})
	if n := strings.Count(got, "| web |"); n != 50 || !strings.Contains(got, "…and 2 more.") {
		t.Fatalf("rows %d:\n%s", n, got)
	}
	if strings.Contains(got, fmt.Sprintf("| #%d |", 51)) {
		t.Fatal("row beyond cap rendered")
	}
}
