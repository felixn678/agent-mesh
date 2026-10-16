package hub

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// ProcessArgs returns the argv of pid; a variable so tests can stand in for
// a hub process.
var ProcessArgs = readProcessArgs

func readProcessArgs(pid int) ([]string, error) {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		if err != nil {
			return nil, err
		}
		return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00"), nil
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return nil, err
	}
	return splitPSCommand(string(out)), nil
}

// splitPSCommand turns `ps -o command=` output (macOS has no /proc) into argv.
// ps joins argv with spaces, so an executable path containing spaces (e.g.
// under "Application Support") would split into pieces; the first word ending
// a path element named like the binary is taken as the end of argv[0].
func splitPSCommand(out string) []string {
	cmd := strings.TrimSpace(out)
	if i := strings.Index(cmd, "agent-mesh"); i >= 0 {
		end := len(cmd)
		if j := strings.IndexByte(cmd[i:], ' '); j >= 0 {
			end = i + j
		}
		return append([]string{cmd[:end]}, strings.Fields(cmd[end:])...)
	}
	return strings.Fields(cmd)
}

// isHubArgs reports whether argv looks like `.../agent-mesh hub ...`.
func isHubArgs(args []string) bool {
	if len(args) < 2 || !strings.Contains(filepath.Base(args[0]), "agent-mesh") {
		return false
	}
	for _, a := range args[1:] {
		if a == "hub" {
			return true
		}
	}
	return false
}

// checkHubProcess refuses pids that are not a running agent-mesh hub.
func checkHubProcess(pid int) error {
	args, err := ProcessArgs(pid)
	if err != nil {
		return fmt.Errorf("hub pid %d is not running: %w", pid, err)
	}
	if !isHubArgs(args) {
		return fmt.Errorf("pid %d is not an agent-mesh hub process (%q); refusing to signal it", pid, strings.Join(args, " "))
	}
	return nil
}
