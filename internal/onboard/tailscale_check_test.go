package onboard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Mac app does not put `tailscale` on PATH; doctor must still find its CLI.
func TestTailscaleCheckFindsMacAppCLI(t *testing.T) {
	ctx := context.Background()
	e, _, _ := newTestEnv(t, "mac")
	e.GOOS = "darwin"
	orig := macTailscaleCLI
	t.Cleanup(func() { macTailscaleCLI = orig })
	macTailscaleCLI = filepath.Join(t.TempDir(), "Tailscale")

	if r := tailscaleCheck(ctx, e); r.Status != Warn || !strings.Contains(r.Detail, "not installed") {
		t.Fatalf("no app: %+v", r)
	}
	if err := os.WriteFile(macTailscaleCLI, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if r := tailscaleCheck(ctx, e); r.Status != Warn || !strings.Contains(r.Detail, "not connected") {
		t.Fatalf("app, disconnected: %+v", r)
	}
	e.Runner.(*fakeRunner).outs[macTailscaleCLI+" ip -4"] = "100.64.1.2\n"
	if r := tailscaleCheck(ctx, e); r.Status != Pass || r.Detail != "tailnet address 100.64.1.2" {
		t.Fatalf("app, connected: %+v", r)
	}
	e.GOOS = "linux" // the app path means nothing elsewhere
	if r := tailscaleCheck(ctx, e); !strings.Contains(r.Detail, "not installed") {
		t.Fatalf("linux: %+v", r)
	}
}
