---
phase: 1
title: "Hub core and lease queue"
status: pending
priority: P1
effort: "7h"
dependencies: []
---

# Phase 1: Hub core and lease queue

## Overview

Go module skeleton, SQLite store with fenced lease queue, hub HTTP server (REST, per-host + admin tokens, reaper, hardening), shared wire types, REST client, test harness, and **frozen contracts** so phases 2/3/4 run in parallel without touching phase-1 files.

Context: [go-hub-stack report](../reports/researcher-261016-1933-go-hub-stack-report.md) §2, §3, §6 · red-team reports in [reports/](./reports/)

## Requirements

Functional
- Task ops: add, get, list (state, updated-since; capped 500 rows), cancel, requeue (stuck/cancelled → queued, attempts reset, fence untouched — next claim bumps it).
- Lease ops: claim (filtered by host caps), heartbeat, complete, fail(retry), **release** (back to queued, attempt not consumed).
- complete/fail/release **idempotent**: replay of an already-applied outcome with same fence+host → 200 `{"already_applied":true}`.
- 409 bodies carry a code: `lease_lost` (fence/host moved on) or `cancelled`.
- Reaper (60 s): (a) running + expired + attempts>=max → `stuck`; (b) running + expired > `stale_requeue` (1 h) + attempts<max → `queued`, fence+1 (kills zombie), reason `lease expired on <host>; requeued`; (c) writes `meta.last_alive_at`.
- Heartbeat revival cap: expired-but-unreclaimed lease revivable only within `revive_window` (30 min).
- Startup grace: only leases alive at `last_alive_at`, once per downtime (see SQL).
- Host load: `POST /hosts/load` every daemon poll tick (hardware + load + `throttle_reason`), also piggybacked on claim/heartbeat. `api.HostLoad{Cores, RAMGB, CPUModel, Slots, SlotsOverridden, CPUPct, MemAvailPct, AgentWorktrees, Running, ThrottleReason, ClaudeVersion, AgentVersion}` — frozen contract (phases 2/3/4 read/write it). `func (h api.Host) Summary() string` → `<name>: 8c/8GB → 1 slot[ (override)][, throttled: <reason>]`, shared by CLI/MCP `hosts` and the report (unit-tested here).
- Auth: **admin token** (humans, CLI, MCP: task CRUD, hosts, report) and **per-host tokens** (lease + load endpoints only). Hub derives host name from the token; no client-asserted `host` field.
- `agent-mesh token new --admin | --host <name> --out <file>`: writes 32-byte hex token 0600, prints `sha256:<hex>` line for hub.toml.

Non-functional
- Pure Go, `CGO_ENABLED=0`, linux/amd64 + darwin/arm64; `go 1.26` in go.mod (modernc/gopsutil need 1.26.0; red-team spike).
- Hub clock only (unix ms), injectable for tests.
- `http.Server{ReadHeaderTimeout:5s, ReadTimeout:30s, IdleTimeout:120s}`; `MaxBytesReader` 128 KB on every handler (413); list cap 500.
- Files < 200 LOC; stdlib `flag` + `net/http` patterns. Priority field **deferred** (FIFO by id).

## Architecture

### Layout (phase 1 creates; owner after phase 1 in brackets)
```
cmd/agent-mesh/main.go          subcommands: hub|daemon|task|hosts|report|token          [P1]
internal/version/version.go     var Version = "dev" (set by -ldflags -X)                    [P1]
internal/api/types.go           TaskSpec, Task, Host, HostLoad, Claim/Heartbeat/Complete/Fail/Release req+resp, ErrorCode [P1]
internal/api/validate.go        TaskSpec.Validate(), ValidHostName, ValidBranch, ValidPRURL [P1]
internal/store/db.go            Open(path) (*sql.DB), migrate via PRAGMA user_version         [P1]
internal/store/store.go         New(db *sql.DB, now func() time.Time, cfg LeaseConfig) *Store [P1]
internal/store/schema.sql       //go:embed                                                  [P1]
internal/store/tasks.go         Add/Get/List/Cancel/Requeue/ListForReport                   [P1]
internal/store/lease.go         Claim/Heartbeat/Complete/Fail/Release                       [P1]
internal/store/lease_maint.go   Reap/Grace/TouchAlive                                       [P1]
internal/store/hosts.go         UpsertHostLoad/ListHosts                                    [P1]
internal/hub/config.go          hub.toml                                                    [P1]
internal/hub/server.go          NewServer(cfg Config, st *store.Store) *Server; Handler(); Serve(ctx, ln) [P1]
internal/hub/auth.go            token→principal (admin | host:<name>) middleware            [P1]
internal/hub/handlers_tasks.go  task CRUD, hosts, report                                    [P1]
internal/hub/handlers_lease.go  claim/heartbeat/complete/fail/release/hosts load            [P1]
internal/client/client.go       REST client; ErrNoTask, *LeaseError{Code}; per-call timeout [P1]
internal/config/load.go         LoadTOML, DefaultDir, ReadToken, NewToken, HashToken        [P1]
internal/hubtest/hubtest.go     Start(t) *Hub{URL, Store, Clock, AdminToken, HostToken(name)}; Restart() same port; Stop() [P1]
--- frozen contract stubs (signature fixed; body replaced by owner) ---
internal/mcptools/handler.go    func NewHandler(st *store.Store) http.Handler               [P2]
internal/cli/run.go             func Run(ctx context.Context, args []string) error          [P2]
internal/daemon/run.go          func Run(ctx context.Context, args []string) error          [P3]
internal/report/build.go        type Input{Now, Since time.Time; Tasks []api.Task; Hosts []api.Host}
                                func Build(in Input) string  (pure)                        [P4]
internal/report/run.go          func Run(ctx context.Context, args []string) error          [P4]
```
Frozen also: `store.ListForReport(ctx, since time.Time) ([]api.Task, error)` (updated>=since ∪ non-terminal; used by REST + MCP), `hub.NewServer`, `store.New` with injectable clock, `version.Version`, `hubtest` API. Deps added in P1: modernc sqlite, BurntSushi/toml, go-sdk (stub uses `mcp.NewStreamableHTTPHandler`).

### Data flow
```
admin (CLI/MCP) --add--> hub --INSERT--> tasks(queued)
daemon[host token] --claim{caps,load}--> hub --UPDATE..RETURNING--> task+fence | 204
daemon --hosts/load{load,throttle_reason} every tick--> hosts row
daemon --heartbeat{fence,load}/60s--> 200 | 409{lease_lost|cancelled}
daemon --complete|fail|release{fence,...}--> 200 | 200 already_applied | 409{code}
reaper/60s --> stuck at cap | requeue stale (fence+1) | last_alive_at
```

### Schema (`schema.sql`, user_version=1)
```sql
CREATE TABLE tasks (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,  -- ids never reused (branch names embed id)
  repo             TEXT NOT NULL,                 -- logical key; host maps to clone URL
  base_branch      TEXT NOT NULL DEFAULT 'main',
  prompt           TEXT NOT NULL,
  permission_mode  TEXT NOT NULL DEFAULT 'acceptEdits' CHECK (permission_mode IN ('acceptEdits','dontAsk','plan')),
  tool_profile     TEXT NOT NULL DEFAULT 'default', -- name only; host resolves to tools
  max_budget_usd   REAL NOT NULL,
  host             TEXT,                          -- optional constraint
  state            TEXT NOT NULL DEFAULT 'queued'
                   CHECK (state IN ('queued','running','done','stuck','cancelled')),
  claimed_by TEXT, fence INTEGER NOT NULL DEFAULT 0,
  attempts INTEGER NOT NULL DEFAULT 0, max_attempts INTEGER NOT NULL DEFAULT 3,
  lease_expires_at INTEGER, last_heartbeat_at INTEGER,
  branch TEXT, pr_url TEXT, session_id TEXT, worktree TEXT,
  cost_usd REAL NOT NULL DEFAULT 0,               -- summed over attempts
  publish_note TEXT,                              -- e.g. leftover_commit, no_changes, pr_failed
  reason TEXT,                                    -- last failure/stuck/release reason (redacted)
  created_by TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, finished_at INTEGER
);
CREATE INDEX tasks_claim ON tasks(state, id);
CREATE INDEX tasks_updated ON tasks(updated_at);
CREATE TABLE hosts (
  name TEXT PRIMARY KEY,
  cores INTEGER, ram_gb INTEGER, cpu_model TEXT,          -- hardware, sent by daemon (cpu_model ≤100 chars)
  slots INTEGER, slots_overridden INTEGER NOT NULL DEFAULT 0, -- effective max_slots; 1 if config override
  cpu_pct REAL, mem_avail_pct REAL, agent_worktrees INTEGER,
  running INTEGER, throttle_reason TEXT, claude_version TEXT, agent_version TEXT,
  last_seen_at INTEGER NOT NULL
);
CREATE TABLE meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL);  -- last_alive_at, last_grace_at
```
DSN: `file:<db_path>?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(ON)` (FULL: a returned fence must survive power loss); `SetMaxOpenConns(1)`. DSN + RETURNING + json_each verified by red-team spike (SQLite 3.53.4).

### Key SQL
Claim (single statement = atomic; RHS sees pre-update values):
```sql
UPDATE tasks SET state='running', claimed_by=:host, fence=fence+1, attempts=attempts+1,
  lease_expires_at=:now+:ttl, last_heartbeat_at=:now, updated_at=:now,
  reason = CASE WHEN state='running' THEN 'lease expired on '||claimed_by ELSE reason END
WHERE id = (SELECT id FROM tasks
  WHERE (state='queued' OR (state='running' AND lease_expires_at < :now))
    AND attempts < max_attempts
    AND repo            IN (SELECT value FROM json_each(:repos))
    AND permission_mode IN (SELECT value FROM json_each(:modes))
    AND tool_profile    IN (SELECT value FROM json_each(:profiles))
    AND max_budget_usd <= :budget_cap
    AND (host IS NULL OR host = :host)
  ORDER BY id LIMIT 1)
RETURNING id, fence, repo, base_branch, prompt, permission_mode, tool_profile, max_budget_usd;
```
Heartbeat (fence is authority; bounded revival):
```sql
UPDATE tasks SET lease_expires_at=:now+:ttl, last_heartbeat_at=:now, updated_at=:now
WHERE id=:id AND fence=:fence AND claimed_by=:host AND state='running'
  AND lease_expires_at >= :now - :revive_window RETURNING id;
```
Complete/fail/release: `WHERE id AND fence AND claimed_by AND state='running'`.
- complete: `state='done'`, branch, pr_url (validated `^https://github\.com/[^/]+/[^/]+/pull/\d+$` or empty), session_id, `cost_usd=cost_usd+:cost`, publish_note, finished_at.
- fail: `state = CASE WHEN :retry AND attempts<max_attempts THEN 'queued' ELSE 'stuck' END`, cost added, reason, worktree, lease cleared.
- release: `state='queued', attempts=attempts-1`, reason `released by <host>: …`, lease cleared.
- 0 rows → `SELECT state, fence, claimed_by`: same fence+host and state already matches the requested outcome → 200 already_applied; same fence+host and `cancelled` → 409 `cancelled`; else 409 `lease_lost`. Heartbeat uses same 409 mapping.

Maintenance:
```sql
-- reaper (a) at cap
UPDATE tasks SET state='stuck', reason='lease expired on '||claimed_by||' at attempt cap',
  lease_expires_at=NULL, finished_at=:now, updated_at=:now
WHERE state='running' AND lease_expires_at < :now AND attempts >= max_attempts;
-- reaper (b) stale below cap: requeue, bump fence so zombie heartbeats 409
UPDATE tasks SET state='queued', fence=fence+1, reason='lease expired on '||claimed_by||'; requeued',
  lease_expires_at=NULL, updated_at=:now
WHERE state='running' AND lease_expires_at < :now - :stale_requeue AND attempts < max_attempts;
-- grace, run once after listen succeeds, only if last_grace_at < last_alive_at
UPDATE tasks SET lease_expires_at = MAX(lease_expires_at, :now + :ttl)
WHERE state='running' AND lease_expires_at >= :last_alive_at;
-- then meta.last_grace_at = :now
```
Grace covers only leases alive when the hub died, and a crash loop graces once (last_alive_at only advances from reaper ticks).

### REST (`/api/v1`, JSON, errors `{"error":..., "code":...}`)
| Method path | Principal | Body → Resp |
|---|---|---|
| POST /tasks | admin | TaskSpec → Task 201 |
| GET /tasks?state=&since= | admin | → []Task (≤500) |
| GET /tasks/{id} | admin | → Task |
| POST /tasks/{id}/cancel, /requeue | admin | → Task |
| GET /hosts | admin | → []Host |
| GET /report?since= | admin | → text/markdown `report.Build(Input{ListForReport, ListHosts})`, default since=16h |
| POST /claim | host | {repos[], modes[], profiles[], budget_cap, load} → ClaimedTask / 204 |
| POST /hosts/load | host | HostLoad (hardware + load + throttle_reason) → 204 |
| POST /tasks/{id}/heartbeat | host | {fence, load} → 200 / 409 |
| POST /tasks/{id}/complete, /fail, /release | host | → 200 / 409 |
`GET /healthz` no auth. Admin token on a host endpoint → 403 and vice versa.

### Validation (`api`, shared by REST + MCP)
- `repo` `^[A-Za-z0-9._-]{1,64}$`; `host` `^[a-z0-9-]{1,32}$`; `tool_profile` same as host.
- `base_branch`: `^[A-Za-z0-9._/-]{1,100}$`, not starting `-`, no `..`, no `//`, not ending `/` or `.lock`, **not prefixed `agentmesh/`**.
- `permission_mode` ∈ {acceptEdits, dontAsk, plan}; `bypassPermissions`, `auto`, `manual` rejected.
- budget 0 < b ≤ 100; prompt non-empty ≤ 64 KB; max_attempts 1..5.

### Hub config (`hub.toml`, placeholders)
```toml
listen = "127.0.0.1:8787"          # v1 Ubuntu-only; later "<hub-tailscale-ip>:8787"
db_path = "~/.local/share/agent-mesh/hub.db"
admin_token_hash = "sha256:<hex>"
lease_ttl = "5m"; reap_interval = "60s"; stale_requeue = "1h"; revive_window = "30m"
[host_tokens]
<host-name> = "sha256:<hex>"
```
Auth: sha256(presented) compared to each stored hash with `subtle.ConstantTimeCompare`. Startup: open → migrate → **listen (retry bind every 5 s up to 2 min)** → grace → reaper → serve; SIGTERM → `Shutdown` 10 s.

## Related Code Files
Create: all files in layout + `go.mod`, `go.sum`, `.gitignore` (`/bin/`, `*.db`, `*.db-*`, `*.token`, `.envrc`). Modify/Delete: none (greenfield).

## Implementation Steps
1. Spike already done by red team (modernc DSN/WAL/RETURNING/json_each, gopsutil darwin no-cgo, go-sdk v1.8.0 `Stateless`, `NewInMemoryTransports`). Re-run the darwin cross-compile once after `go mod init` to confirm pins.
2. `go mod init <module-path>` (open question), `go 1.26`, add deps pinned.
3. `internal/version`, `internal/api` types + validation (table tests).
4. `internal/store`: db, schema, tasks, lease, maint, hosts.
5. Store tests (below), incl. race test.
6. `internal/config` + `token new`.
7. `internal/hub`: config, auth, handlers, server (timeouts, MaxBytesReader, bind retry, startup order).
8. `internal/client`: typed methods; per-call timeout 30 s; 204→ErrNoTask; 409→`*LeaseError{Code}`; network errors raw.
9. `internal/hubtest`: fake clock, fixed-port restart (save `ln.Addr()`, re-`net.Listen` same addr on Restart), tokens pre-provisioned.
10. Contract stubs; `main.go`.
11. `go vet ./... && go test -race ./...`; cross-compile both targets.

## Success Criteria
- [ ] `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...` and linux/amd64 succeed.
- [ ] Store tests on real SQLite file in `t.TempDir()`:
  - claim empty → nil; FIFO; host constraint; caps filter (repo, mode, profile, budget) excludes non-matching.
  - stale fence / other host heartbeat → 409 `lease_lost`; cancelled → `cancelled`.
  - expired lease reclaimed: fence+1, attempts+1, reason recorded; old holder → `lease_lost`.
  - revival: expired 10 min unreclaimed → heartbeat OK; expired 40 min → `lease_lost`.
  - complete twice same fence → second 200 already_applied; fail/release replay same.
  - fail retry below cap → queued; at cap or retry=false → stuck; cost summed. release → attempts unchanged net.
  - reaper (a) stuck at cap; (b) stale requeue bumps fence. Grace: only alive-at-crash leases; second start without reaper tick → no re-grace.
  - AUTOINCREMENT: delete max row, add → new id > deleted.
- [ ] **Race test** (`-race`): 4 `*sql.DB` handles on one file, 200 tasks, 16 claimers → each task claimed exactly once, unique (id,fence), no SQLITE_BUSY surfaced.
- [ ] HTTP tests via `hubtest`: 401 no/wrong token; admin↔host 403; host derived from token (body host ignored); 413 on 1 MB body; `/healthz` open; claim→heartbeat→complete via client; 204 empty; restart on same port keeps leases (grace).
- [ ] Validation table: `bypassPermissions`, `auto`, `-x` branch, `agentmesh/task-1-f1` base, bad pr_url all rejected.

## Risk Assessment
| Risk | L×I | Mitigation |
|---|---|---|
| Frozen contract incomplete → parallel phases blocked | M×H | freeze list above incl. ListForReport, NewServer, hubtest; change needs lead approval + plan.md note |
| SQLITE_BUSY in race test | M×L | busy_timeout 5 s; test proves |
| Power loss loses committed fence | L×H | synchronous=FULL |
| Restore from backup reissues ids/fences | L×M | AUTOINCREMENT; runbook: mark restored `running` rows stuck "restored"; daemon push uses `--force-with-lease=<branch>:` so collisions fail loudly |
| Bind fails at boot (addr not up yet) | M×H | bind retry 2 min, then exit → systemd RestartSec=10, no start limit (phase 5) |
| Token theft | M×H | per-host tokens scoped to lease endpoints; admin separate; hashes only in hub.toml |
| Rollback | — | greenfield; revert commits; delete DB file. Future schema: forward-only `user_version` migrations |
