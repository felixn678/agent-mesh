package hub

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/felixn678/agent-mesh/internal/config"
	"github.com/felixn678/agent-mesh/internal/logfile"
	"github.com/felixn678/agent-mesh/internal/store"
)

// Bind retry policy: the tailnet/loopback address may not exist yet at boot.
// Variables so tests can shorten them.
var (
	bindRetryEvery = 5 * time.Second
	bindRetryMax   = 2 * time.Minute
)

// ListenWithRetry binds addr, retrying every interval until max elapses.
func ListenWithRetry(ctx context.Context, addr string, every, max time.Duration) (net.Listener, error) {
	deadline := time.Now().Add(max)
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln, nil
		}
		if time.Now().Add(every).After(deadline) {
			return nil, fmt.Errorf("listen %s: %w", addr, err)
		}
		slog.Warn("bind failed, retrying", "addr", addr, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(every):
		}
	}
}

// Run is the `agent-mesh hub` entry point: open and migrate the database,
// bind (with retry), then grace, reaper and serve until ctx is cancelled.
//
// SIGHUP handling is installed first, so a reload requested while the hub is
// still opening the database or waiting for its address is queued (not fatal)
// and applied once serving starts.
func Run(ctx context.Context, args []string) error {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	dir, err := config.DefaultDir()
	if err != nil {
		return err
	}
	path := fs.String("config", filepath.Join(dir, "hub.toml"), "path to hub.toml")
	logPath := fs.String("log-file", "", "write logs to this file, rotated at 10 MB x 3 (for launchd; systemd uses the journal)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *logPath != "" {
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
	dbPath, err := config.ExpandHome(cfg.DBPath)
	if err != nil {
		return err
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	ln, err := ListenWithRetry(ctx, cfg.Listen, bindRetryEvery, bindRetryMax)
	if err != nil {
		if ctx.Err() != nil { // stopped while waiting for the address: a clean shutdown
			return nil
		}
		return err
	}
	defer ln.Close()
	slog.Info("hub listening", "addr", ln.Addr().String())
	srv := NewServer(cfg, store.New(db, nil, cfg.Lease()))
	defer srv.watchReload(ctx, *path, hup)()
	return srv.Serve(ctx, ln)
}
