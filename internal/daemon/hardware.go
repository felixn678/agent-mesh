package daemon

import (
	"math"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

// Hardware is sampled once at startup and reported with every load tick.
type Hardware struct {
	Cores    int
	RAMGB    int
	CPUModel string
}

// DefaultSlots derives concurrent task slots from hardware: reserve ~8 GB
// for the OS and human work, then ~4 cores + ~4 GB per claude task, clamped
// to 1..4. Integer division throughout.
func DefaultSlots(cores, ramGB int) int {
	return max(1, min(cores/4, (ramGB-8)/4, 4))
}

// slotsFor applies the max_slots override (0 = auto).
func slotsFor(maxSlots int, hw Hardware) (slots int, overridden bool) {
	if maxSlots > 0 {
		return maxSlots, true
	}
	return DefaultSlots(hw.Cores, hw.RAMGB), false
}

// detectHardware reads logical cores, total RAM (rounded GiB) and CPU model.
func detectHardware() Hardware {
	hw := Hardware{CPUModel: "unknown"}
	if n, err := cpu.Counts(true); err == nil {
		hw.Cores = n
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		hw.RAMGB = int(math.Round(float64(vm.Total) / (1 << 30)))
	}
	if info, err := cpu.Info(); err == nil && len(info) > 0 && info[0].ModelName != "" {
		hw.CPUModel = info[0].ModelName
	}
	if len(hw.CPUModel) > 100 {
		hw.CPUModel = hw.CPUModel[:100]
	}
	return hw
}
