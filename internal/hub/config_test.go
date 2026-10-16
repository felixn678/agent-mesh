package hub_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/config"
	"github.com/felixn678/agent-mesh/internal/hub"
	"github.com/felixn678/agent-mesh/internal/hubtest"
)

func TestBodyHostFieldIgnored(t *testing.T) {
	h := hubtest.Start(t)
	addTask(t, h)
	status, body := do(t, "POST", h.URL+"/api/v1/claim", h.HostToken("host-a"),
		`{"host":"host-b","repos":["r"],"modes":["acceptEdits"],"profiles":["default"],"budget_cap":10}`)
	if status != 200 {
		t.Fatalf("%d %s", status, body)
	}
	tasks, _ := h.Admin().ListTasks(bg, api.StateRunning, time.Time{})
	if len(tasks) != 1 || tasks[0].ClaimedBy != "host-a" {
		t.Fatalf("body host honoured: %+v", tasks)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "hub.toml")
	toml := "admin_token_hash = \"" + config.HashToken("a") + "\"\nlease_ttl = \"2m\"\n" +
		"[host_tokens]\nbox = \"" + config.HashToken("b") + "\"\n"
	os.WriteFile(good, []byte(toml), 0o600)
	c, err := hub.LoadConfig(good)
	if err != nil || c.LeaseTTL != 2*time.Minute || c.Listen != "127.0.0.1:8787" || c.StaleRequeue != time.Hour {
		t.Fatalf("%+v %v", c, err)
	}
	bad := map[string]string{
		"no admin":   "lease_ttl = \"1m\"",
		"bad hash":   "admin_token_hash = \"sha256:zz\"",
		"admin host": "admin_token_hash = \"" + config.HashToken("a") + "\"\n[host_tokens]\nadmin = \"" + config.HashToken("b") + "\"",
		"bad name":   "admin_token_hash = \"" + config.HashToken("a") + "\"\n[host_tokens]\n\"Bad Name\" = \"" + config.HashToken("b") + "\"",
		"typo":       "admin_token_hash = \"" + config.HashToken("a") + "\"\nlisten_addr = \"x\"",
		"zero ttl":   "admin_token_hash = \"" + config.HashToken("a") + "\"\nlease_ttl = \"0s\"",
	}
	for name, body := range bad {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".toml")
		os.WriteFile(p, []byte(body), 0o600)
		if _, err := hub.LoadConfig(p); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestListenWithRetryWaitsForAddress(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := blocker.Addr().String()
	go func() { time.Sleep(300 * time.Millisecond); blocker.Close() }()
	ln, err := hub.ListenWithRetry(context.Background(), addr, 100*time.Millisecond, 5*time.Second)
	if err != nil {
		t.Fatalf("never bound: %v", err)
	}
	ln.Close()

	blocker2, _ := net.Listen("tcp", "127.0.0.1:0")
	defer blocker2.Close()
	if _, err := hub.ListenWithRetry(context.Background(), blocker2.Addr().String(), 50*time.Millisecond, 200*time.Millisecond); err == nil {
		t.Fatal("expected bind failure after max wait")
	}
}

func TestConfigRejectsReusedTokenHash(t *testing.T) {
	c := hub.DefaultConfig()
	c.AdminTokenHash = config.HashToken("a")
	c.HostTokens = map[string]string{"box": config.HashToken("b")}
	c.SessionTokens = map[string]string{"box": config.HashToken("b")} // same secret for daemon and sessions
	if err := c.Validate(); err == nil {
		t.Fatal("shared hash accepted")
	}
	c.SessionTokens["box"] = config.HashToken("c")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

// Stopping the hub while it still waits for its address is a clean shutdown
// (exit 0), not an error a service manager would treat as a crash.
func TestRunStoppedDuringBindRetryReturnsNil(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.toml")
	body := "listen = \"" + blocker.Addr().String() + "\"\ndb_path = \"" + filepath.Join(dir, "hub.db") +
		"\"\nadmin_token_hash = \"" + config.HashToken("admin") + "\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- hub.Run(ctx, []string{"--config", path}) }()
	time.Sleep(200 * time.Millisecond) // inside the bind retry loop
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancel during bind retry: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}
