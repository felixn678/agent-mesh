package hub

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"time"

	"github.com/felixn678/agent-mesh/internal/config"
	"github.com/felixn678/agent-mesh/internal/store"
)

// Bind retry policy: the tailnet/loopback address may not exist yet at boot.
const (
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
func Run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	dir, err := config.DefaultDir()
	if err != nil {
		return err
	}
	path := fs.String("config", filepath.Join(dir, "hub.toml"), "path to hub.toml")
	if err := fs.Parse(args); err != nil {
		return err
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
		return err
	}
	defer ln.Close()
	slog.Info("hub listening", "addr", ln.Addr().String())
	return NewServer(cfg, store.New(db, nil, cfg.Lease())).Serve(ctx, ln)
}
