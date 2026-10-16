# Codebase summary

One Go module (`github.com/felixn678/agent-mesh`), one binary (`agent-mesh`). `cmd/agent-mesh/main.go` dispatches the subcommand to a package below.

| Package | Role | Start reading at |
|---|---|---|
| `cmd/agent-mesh` | entry point, subcommand dispatch, `token new` | `main.go`, `token.go` |
| `internal/api` | wire types shared by hub, clients and daemon; input validation; repo key normalization (`CanonicalRepo`) | `types.go`, `validate.go`, `repo.go` |
| `internal/store` | hub persistence on SQLite (WAL, `synchronous=FULL`, one connection): tasks, fenced leases, idempotent claims, reaper, hosts, sessions, messages, inbox long-poll | `schema.sql`, `lease.go`, `claim_session.go`, `lease_maint.go` |
| `internal/hub` | HTTP server: REST `/api/v1`, MCP mount `/mcp`, token auth (admin/host/session), reaper loop, `SIGHUP` token reload, `hub.toml` | `server.go`, `auth.go`, `handlers_*.go` |
| `internal/client` | typed REST client used by CLI, hooks and daemon; typed lease errors | `client.go` |
| `internal/config` | TOML loading (unknown keys rejected), config dir, token files, hub URL resolution | `load.go`, `hub_url.go` |
| `internal/mcptools` | agentmesh MCP tools (`task_*`, `sessions`, `send`, `inbox`, `hosts`, `morning_report`); `format/` renders compact text | `handler.go`, `tools_task.go`, `tools_session_tasks.go` |
| `internal/cli` | human subcommands `task`, `hosts`, `inbox --wait`; Claude Code hooks (`hook session-start|prompt|stop|session-end|mcp-headers`); `setup-claude` (byte-splicing settings merge); `hubenv/` resolves URL and tokens | `run.go`, `hook_cmds.go`, `setup_claude.go` |
| `internal/daemon` | per-host pull worker: config and caps, hardware slots and load, claim loop with circuit breaker, daemon-owned clones and worktrees, isolated `claude -p` runner, write-ahead publish journal (scan, push, supersede, draft PR, complete), orphan sweep, GC | `run.go`, `loop.go`, `task_exec.go`, `publish.go` |
| `internal/report` | morning report (needs attention, hosts, queues, cost); `report --out` guard | `build.go`, `sections.go` |
| `internal/onboard` | `up`, `invite`, `join`, `doctor`: config and token files, `hub.toml` edits, invite bundles, systemd/launchd user services | `up.go`, `invite.go`, `join.go`, `doctor.go` |
| `internal/version` | build version (set by `make build` via ldflags) | `version.go` |
| `internal/hubtest` | test helper: real hub on loopback with a real SQLite file and a fake clock | `hubtest.go` |
| `internal/e2e` | end-to-end tests: built binary, two simulated hosts, sessions and daemons | `two_hosts_test.go`, `scenarios_test.go` |

Other directories:

| Path | Contents |
|---|---|
| `deploy/` | `install.sh` (manual install), example `hub.toml` / `daemon.toml` (placeholders only), systemd units, launchd plist |
| `docs/` | architecture, deployment guide, this summary |
| `plans/` | implementation plans and review reports (history, not user docs) |

Tests: `make test` (`go vet` + `go test -race ./...`). Daemon tests use the test binary itself as fake `claude`, `gh` and (if absent) `gitleaks` via symlinks, plus temp bare git repos; no real remotes are touched.
