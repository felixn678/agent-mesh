# Go hub stack: research report

Date: 2026-10-16. Scope: MCP SDK, SQLite driver, lease pattern, host metrics, discovery, MCP transport architecture.
Web lookups: 5 (MCP go-sdk x2, mcp-go, modernc sqlite, ncruces). Items marked (UNVERIFIED) are from memory, not checked against current docs.

## Verdicts (ranked)

1. MCP SDK: **official `github.com/modelcontextprotocol/go-sdk`** (v1.8.0 seen, Sep 2026).
2. SQLite: **`modernc.org/sqlite`** (v1.60.1 seen, Sep 2026), pure Go, no cgo.
3. Claim: **single-statement `UPDATE ... RETURNING` with fence counter**. No separate lock.
4. Architecture: **hub serves Streamable HTTP MCP directly**; `.mcp.json` uses `url`. No stdio shim.
5. Discovery: **skip mDNS code**. Use `<hub-host>.local` (OS-level avahi) or DHCP reservation + static config.
6. Host metrics: **gopsutil v4** (UNVERIFIED). Worktree count via `git worktree list --porcelain`.

## 1. MCP SDK

| | official go-sdk | mark3labs/mcp-go |
|---|---|---|
| Latest seen | v1.8.0, pub 2026-09-04 (pkg.go.dev, not latest per page) | no release info on page |
| Stdio | yes: `mcp.StdioTransport{}`, `server.Run(ctx, t)` | yes: `server.ServeStdio` |
| Streamable HTTP | yes: `mcp.NewStreamableHTTPHandler(getServer, opts)`, has `ServeHTTP(w, r)` | yes (page claims stdio, SSE, streamable-HTTP) |
| Governance | official MCP org repo | community project |
| Stability signal | v1.x line, middleware API (`AddReceivingMiddleware`) | "under active development; core working, advanced in progress" |
| Auth helpers | none seen on pkg page (checked first 100k chars only) | not checked |

Minimal tool registration (official, from README):
```go
server := mcp.NewServer(&mcp.Implementation{Name: "agentmesh", Version: "v0.1.0"}, nil)
mcp.AddTool(server, &mcp.Tool{Name: "claim_task", Description: "claim next task"}, claimTask)
// claimTask: func(ctx context.Context, req *mcp.CallToolRequest, in ClaimIn) (*mcp.CallToolResult, ClaimOut, error)
// in/out structs carry json + jsonschema tags
server.Run(ctx, &mcp.StdioTransport{})
```
HTTP mode: wrap `mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {...}, nil)` in `http.Server` with bearer middleware.

Trade-off: go-sdk is the reference for MCP spec changes; mcp-go is more feature-rich historically but governance and spec tracking are weaker. Pick go-sdk. Risk: API churn across v1.x minors; pin version in go.mod.

## 2. SQLite driver (no cgo, cross-compile)

| | modernc.org/sqlite | ncruces/go-sqlite3 | mattn/go-sqlite3 |
|---|---|---|---|
| Latest seen | v1.60.1, pub 2026-09-29, SQLite 3.53.x | not shown | not checked |
| cgo | none | none (Wasm, wasm2go) | required |
| darwin/arm64 | yes (20 GOOS/GOARCH pairs) | yes | needs cgo toolchain |
| database/sql name | `sqlite` | `sqlite3` | `sqlite3` |
| Memory | normal | higher per connection (Wasm sandbox, per README) | normal |
| Ecosystem | largest pure-Go user base (UNVERIFIED) | smaller | largest overall |

Rank: modernc > ncruces > mattn. mattn forces cross-compile toolchain for Mac; not worth it. ncruces is viable but smaller user base and higher memory; no benefit at this load.

DSN (modernc, verify with one test run):
```
file:/var/lib/agentmesh/hub.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)
```
Per modernc docs `_pragma` values get `PRAGMA` prepended. ncruces shorthand `_busy_timeout` / `_journal_mode` exists but was not confirmed for `_pragma`.

Pool: `db.SetMaxOpenConns(1)`. One writer, no SQLITE_BUSY on upgrade. Volume is tens of writes/hour; this is enough.

RETURNING: SQLite >= 3.35. modernc ships 3.5x. Supported.

## 3. Lease / claim pattern

Rules:
- Clock: hub only (`now` = hub unix ms). Mac/Ubuntu clock skew irrelevant.
- Fence: `fence` increments on every claim. Every heartbeat/complete/fail must match `owner` + `fence`. Stale holder gets 0 rows → must stop.
- Attempt cap: `max_attempts`. Expired lease at cap → `stuck`, not requeued.
- Completion after expiry accepted only if fence still matches and no reclaim happened.

Schema:
```sql
CREATE TABLE tasks (
  id              INTEGER PRIMARY KEY,
  payload         TEXT NOT NULL,                 -- JSON: repo, branch base, prompt, allowed cmd id
  state           TEXT NOT NULL CHECK (state IN ('queued','running','done','failed','stuck')),
  owner           TEXT,                          -- host/session id
  fence           INTEGER NOT NULL DEFAULT 0,    -- increments per claim
  attempts        INTEGER NOT NULL DEFAULT 0,
  max_attempts    INTEGER NOT NULL DEFAULT 3,
  lease_expires_at INTEGER,                      -- unix ms, hub clock
  last_heartbeat_at INTEGER,
  result          TEXT,
  reason          TEXT,                          -- why failed/stuck
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);
CREATE INDEX tasks_claimable ON tasks(state, lease_expires_at);
```

Claim (one statement = atomic in SQLite; no explicit lock):
```sql
UPDATE tasks
SET state='running', owner=:owner, fence=fence+1, attempts=attempts+1,
    lease_expires_at=:now+:ttl, last_heartbeat_at=:now, updated_at=:now
WHERE id = (
  SELECT id FROM tasks
  WHERE (state='queued' OR (state='running' AND lease_expires_at < :now))
    AND attempts < max_attempts
  ORDER BY id LIMIT 1
)
RETURNING id, fence, payload;
```
Lazy reclaim of expired leases happens here. Empty result = nothing claimable.

Heartbeat (rejects stale holder):
```sql
UPDATE tasks SET lease_expires_at=:now+:ttl, last_heartbeat_at=:now, updated_at=:now
WHERE id=:id AND owner=:owner AND fence=:fence AND state='running' AND lease_expires_at>=:now
RETURNING id;
```
0 rows → lease lost, worker aborts.

Complete / fail:
```sql
UPDATE tasks SET state='done', result=:result, owner=NULL, lease_expires_at=NULL, updated_at=:now
WHERE id=:id AND owner=:owner AND fence=:fence AND state='running' RETURNING id;

UPDATE tasks SET state=CASE WHEN attempts>=max_attempts THEN 'stuck' ELSE 'queued' END,
    reason=:reason, owner=NULL, lease_expires_at=NULL, updated_at=:now
WHERE id=:id AND owner=:owner AND fence=:fence AND state='running' RETURNING id, state;
```

Reaper (every 60s, for morning report; claim already reclaims):
```sql
UPDATE tasks SET state='stuck', reason='lease expired at attempt cap', owner=NULL, updated_at=:now
WHERE state='running' AND lease_expires_at < :now AND attempts >= max_attempts;
```

Timing: TTL 5 min, heartbeat every 60 s. Mac lid-close pauses heartbeat → lease expires → task reclaimed → Mac wakes, its heartbeat/complete gets 0 rows → stops. Good.

Known gap (important): fence protects hub state only. A zombie worker can still push a branch or open a PR. Mitigate: branch name includes `task-<id>-f<fence>`; PR creation done by hub after complete with matching fence, not by worker. Do not let workers open PRs directly.

## 4. Host load metrics

- gopsutil v4 (`github.com/shirou/gopsutil/v4`): `cpu.Percent(interval, false)`, `mem.VirtualMemory()`, `load.Avg()`. Linux + darwin. (UNVERIFIED: no cgo needed for these on darwin in v4; confirm with `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build`.)
- Worktrees (cheap, no gopsutil): `git -C <repo> worktree list --porcelain | grep -c '^worktree '`. Count Claude-owned worktrees by path prefix.
- Load signal for routing: `load1 / ncpu` plus free RAM. Hub picks min. Keep it to 3 numbers per heartbeat.
- Do not poll more than every 30 s; reporter is a systemd timer or goroutine in host agent. YAGNI: do not build a metrics pipeline.

## 5. Discovery: mDNS

Verdict: **skip for now**.
- 2-3 hosts, fixed LAN. Static `hosts.yaml` (name, ssh target, hub URL) costs 10 lines.
- mDNS cost: grandcat/zeroconf or hashicorp/mdns code, multicast quirks on wifi, Mac/Ubuntu differences, debugging time. (UNVERIFIED maintenance status of both libs.)
- Hub address: use `<hub-host>.local` (avahi already active per handoff) resolved by OS. Or DHCP reservation on router for <hub-lan-ip>. No Go code.
- Revisit only at 4+ hosts or if IPs churn.

## 6. Architecture: thin stdio shim vs direct HTTP

Options:
- A. Per-session stdio MCP process that calls hub REST. Extra process per Claude session, extra failure mode, stdio logs clash with protocol if misused.
- B. Hub serves Streamable HTTP MCP; `.mcp.json`:
```json
{
  "mcpServers": {
    "agentmesh": {
      "type": "http",
      "url": "http://<hub-host>.local:8787/mcp",
      "headers": { "Authorization": "Bearer ${AGENTMESH_TOKEN}" }
    }
  }
}
```
(UNVERIFIED: exact `type`/`headers`/`${VAR}` expansion in current Claude Code docs. Check with one test before committing.)

Rank: **B**. One process (the hub), no per-session binary, no duplicate state. Session identity: pass `owner` as tool arg (`claim_task{host, session}`); no need for transport identity.

Host-local data (load, worktrees) does not belong in MCP. Host reporter = small systemd/launchd timer POSTing JSON to hub REST. Keeps MCP surface to claim/heartbeat/complete/fail/list.

Auth:
- Bearer token in env var `AGENTMESH_TOKEN`; `.mcp.json` references `${AGENTMESH_TOKEN}`. Never commit the value.
- Constant-time compare (`crypto/subtle.ConstantTimeCompare`) in HTTP middleware wrapping the MCP handler. Go SDK showed no auth package (checked first 100k chars of pkg page only).
- Bind to LAN IP only, not 0.0.0.0 if avoidable.
- Concern: plain HTTP over shared wifi sends the token in clear. Dispatch = remote exec. Options: (a) self-signed TLS with CA pinned on both machines; (b) Tailscale later (already in handoff as option). Recommend (a) now if cheap, else accept risk on home LAN and document it.
- Dispatch: allowlisted command IDs only (`task.kind` enum → fixed script), no free-form shell in payload.
- Stateful sessions: Streamable HTTP keeps `Mcp-Session-Id`; hub restart drops sessions. (UNVERIFIED: whether Claude Code reconnects automatically.) Test it.

## Limitations

- Did not verify: gopsutil, zeroconf/hashicorp mdns maintenance, Claude Code `.mcp.json` http syntax, go-sdk auth story beyond first 100k chars, go-sdk minimum Go version (go1.26 installed, likely fine).
- Did not benchmark SQLite drivers. Load is tiny; perf irrelevant.
- Did not test Remote Control cross-machine (handoff item, separate).
- GitHub Issues as task store not evaluated (handoff open question).

## Sources

- https://github.com/modelcontextprotocol/go-sdk
- https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp
- https://github.com/mark3labs/mcp-go
- https://pkg.go.dev/modernc.org/sqlite
- https://github.com/ncruces/go-sqlite3
- Local: plans/handoff-from-lead-session.md

## Unresolved questions

1. Claude Code `.mcp.json` http transport: exact keys and `${VAR}` expansion in headers. Test on Ubuntu first.
2. Does Claude Code auto-reconnect MCP HTTP sessions after hub restart?
3. Accept plain HTTP + bearer on LAN, or add self-signed TLS now?
4. Who opens the PR: hub (recommended) or worker? Affects dispatch allowlist.
5. Which host runs hub when Ubuntu is off? (Handoff open question; no hot standby in scope.)
6. GitHub Issues vs SQLite as task store: still open.

Status: DONE_WITH_CONCERNS
Summary: Pick official go-sdk (MCP, Streamable HTTP via `NewStreamableHTTPHandler`), modernc sqlite (pure Go, darwin/arm64 ok), fenced lease claim via single `UPDATE...RETURNING`, hub serves MCP directly via `.mcp.json` url, skip mDNS code. Report at plans/reports/researcher-261016-1933-go-hub-stack-report.md.
Concerns: gopsutil and Claude Code `.mcp.json` http syntax unverified (budget). Zombie workers can still push branches; fence only guards hub state. Plain HTTP token over wifi is a real exposure for remote exec.
