// Package sysproc holds small helpers for inspecting other processes on
// systems without /proc (macOS).
package sysproc

import "os"

// PS returns the ps binary to run. It prefers the absolute /bin/ps (present on
// macOS and Linux) so process checks keep working when PATH is minimal, as it
// is under launchd, hooks or tests; it falls back to a PATH lookup.
func PS() string {
	if _, err := os.Stat("/bin/ps"); err == nil {
		return "/bin/ps"
	}
	return "ps"
}
