package daemon

import (
	"fmt"
	"os"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

// loadSample is the per-tick view of host pressure.
type loadSample struct {
	CPUPct      float64
	MemAvailPct float64
	Worktrees   int // agent worktrees only (<state_dir>/worktrees/*)
}

// sampler is swapped out in tests.
type sampler func(worktreeDir string) (loadSample, error)

// sampleHost reads CPU % since the previous call, available memory and the
// agent worktree count. Human load on the machine shows up here too.
func sampleHost(worktreeDir string) (loadSample, error) {
	var s loadSample
	pct, err := cpu.Percent(0, false)
	if err != nil || len(pct) == 0 {
		return s, fmt.Errorf("cpu percent: %v", err)
	}
	s.CPUPct = pct[0]
	vm, err := mem.VirtualMemory()
	if err != nil || vm.Total == 0 {
		return s, fmt.Errorf("memory: %v", err)
	}
	s.MemAvailPct = float64(vm.Available) * 100 / float64(vm.Total)
	s.Worktrees, err = countEntries(worktreeDir)
	return s, err
}

func countEntries(dir string) (int, error) {
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	return len(ents), err
}

// maxUnhealthyLen keeps "unhealthy: <err>" within the hub's 100-byte
// throttle_reason limit.
const maxUnhealthyLen = 85

// throttleReason returns "" when a claim is allowed, else the first failing
// check in the order unhealthy, slots, cpu, mem, worktrees.
func throttleReason(unhealthy string, running, slots int, s loadSample, c Config) string {
	switch {
	case unhealthy != "":
		return "unhealthy: " + unhealthy
	case running >= slots:
		return "slots_full"
	case s.CPUPct >= c.MaxCPUPct:
		return fmt.Sprintf("cpu %.0f%%", s.CPUPct)
	case s.MemAvailPct < c.MinMemAvailPct:
		return fmt.Sprintf("mem %.0f%%", s.MemAvailPct)
	case s.Worktrees >= c.MaxAgentWorktrees:
		return "worktrees"
	}
	return ""
}
