package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/felixn678/agent-mesh/internal/api"
)

// pathTools are reported when found on PATH. They hint at what a machine can
// build and test; the user's host note says what it is for.
var pathTools = []string{"node", "bun", "pnpm", "go", "python3", "java", "cargo", "docker", "flutter", "pod"}

// machineProbe holds the lookups detection needs, so tests can fake a machine.
type machineProbe struct {
	goos, goarch, home string
	lookPath           func(string) (string, error)
	exists             func(string) bool
	getenv             func(string) string
}

func localProbe() machineProbe {
	home, _ := os.UserHomeDir()
	return machineProbe{goos: runtime.GOOS, goarch: runtime.GOARCH, home: home, lookPath: exec.LookPath,
		exists: func(p string) bool { _, err := os.Stat(p); return err == nil }, getenv: os.Getenv}
}

// detectMachine reports the OS and the tools found. It only stats files (no
// subprocess), so it fits in the session-start hook's time budget.
func (p machineProbe) detectMachine() api.MachineInfo {
	osName := p.goos
	if osName == "darwin" {
		osName = "macos"
	}
	var tools []string
	found := func(name string, ok bool) {
		if ok {
			tools = append(tools, name)
		}
	}
	onPath := func(name string) bool { _, err := p.lookPath(name); return err == nil }
	// The /usr/bin/xcodebuild shim exists without Xcode, so look for the app.
	found("xcode", p.goos == "darwin" && (p.exists("/Applications/Xcode.app") || p.exists("/Applications/Xcode-beta.app")))
	sdks := []string{p.getenv("ANDROID_HOME"), p.getenv("ANDROID_SDK_ROOT"),
		filepath.Join(p.home, "Library", "Android", "sdk"), filepath.Join(p.home, "Android", "Sdk")}
	inSDK := func(rel string) bool {
		for _, sdk := range sdks {
			if sdk != "" && p.exists(filepath.Join(sdk, rel)) {
				return true
			}
		}
		return false
	}
	found("adb", onPath("adb") || inSDK(filepath.Join("platform-tools", "adb")))
	found("android-emulator", onPath("emulator") || inSDK(filepath.Join("emulator", "emulator")))
	found("chrome", onPath("google-chrome") || onPath("chromium") || onPath("chromium-browser") ||
		(p.goos == "darwin" && p.exists("/Applications/Google Chrome.app")))
	for _, name := range pathTools {
		found(name, onPath(name))
	}
	return api.MachineInfo{OS: osName + "/" + p.goarch, Tools: tools}
}
