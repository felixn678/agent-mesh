// Package daemon is the per-host pull worker: it claims tasks from the hub
// when this host is healthy and under its load limits, runs one isolated
// `claude -p` per task in a worktree of a daemon-owned clone, and publishes
// the result through a write-ahead journal while its lease fence is valid.
package daemon

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/config"
	"github.com/felixn678/agent-mesh/internal/logfile"
)

// Run is the `agent-mesh daemon` entry point (args exclude the subcommand).
func Run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	dir, err := config.DefaultDir()
	if err != nil {
		return err
	}
	path := fs.String("config", filepath.Join(dir, "daemon.toml"), "path to daemon.toml")
	printLoad := fs.Bool("print-load", false, "print hardware, slots and current load as JSON, then exit")
	logPath := fs.String("log-file", "", "write logs to this file, rotated at 10 MB x 3 (for launchd; systemd uses the journal)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *logPath != "" && !*printLoad {
		lf, err := logfile.Setup(*logPath)
		if err != nil {
			return err
		}
		defer lf.Close()
	}
	cfg, err := LoadConfig(*path)
	if err != nil {
		return err
	}
	if *printLoad {
		return printHostLoad(cfg)
	}
	tok, err := config.ReadToken(cfg.HostTokenFile)
	if err != nil {
		return err
	}
	d, err := New(cfg, client.New(cfg.HubURL, tok), nil)
	if err != nil {
		return err
	}
	d.log.Info("daemon starting", "hub", cfg.HubURL, "cores", d.hw.Cores, "ram_gb", d.hw.RAMGB,
		"slots", d.slots, "slots_overridden", d.slotsOverridden)
	return d.Loop(ctx)
}

// printHostLoad shows what this host would report, without a hub or token.
func printHostLoad(cfg Config) error {
	hw := detectHardware()
	slots, over := slotsFor(cfg.MaxSlots, hw)
	s, err := sampleHost(filepath.Join(cfg.StateDir, "worktrees"))
	if err != nil {
		return fmt.Errorf("sample load: %w", err)
	}
	d := &Daemon{hw: hw, slots: slots, slotsOverridden: over}
	l := d.hostLoad(s, 0, throttleReason("", 0, slots, s, cfg))
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(l)
}
