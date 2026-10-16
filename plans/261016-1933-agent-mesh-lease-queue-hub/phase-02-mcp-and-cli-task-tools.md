---
phase: 2
title: "MCP and CLI task tools"
status: pending
priority: P1
effort: "3h"
dependencies: [1]
---

# Phase 2: MCP and CLI task tools

## Overview

Human-facing surfaces to enqueue and inspect work, both using the **admin token**: (a) Streamable HTTP MCP at hub `/mcp`, reachable **only** from Claude sessions opened in the agent-mesh repo via its committed `.mcp.json` (project scope); (b) `agent-mesh task|hosts` CLI over REST. No lease ops via MCP (daemons own leases). Workers never see this MCP (`--strict-mcp-config`, phase 3).

Context: [go-hub-stack report](../reports/researcher-261016-1933-go-hub-stack-report.md) §1, §6 · [dispatch report](../reports/researcher-261016-1933-claude-dispatch-hooks-report.md) §3

## Requirements
- MCP tools (terse descriptions; schemas cost context every session): `task_add`, `task_list`, `task_get`, `task_cancel`, `task_requeue`, `hosts`, `morning_report`.
- Output: compact text lines, e.g. `#12 running <host> repo=agent-mesh f2 a1/3 $0.41`.
- `hosts` (CLI + MCP) one line per host: `<host-name>: 8c/8GB → 1 slot (override), running 1, cpu 85% mem 30%, throttled: cpu 85%, seen 20s ago` (cpu_model on `--verbose`). Prefix comes from `api.Host.Summary()` (phase 1); CLI and MCP append the runtime fields.
- CLI: `agent-mesh task add|list|get|cancel|requeue`, `agent-mesh hosts`. `task add` takes `--prompt` | `--prompt-file` (`-` = stdin), `--repo`, `--base`, `--budget`, `--mode`, `--profile`, `--host`, `--max-attempts`.
- Task spec has **`tool_profile` (name)**, never a tool list. Validation = `api.TaskSpec.Validate()` (phase 1) for both paths.
- `.mcp.json` committed, no secrets, no infra values:
  ```json
  {"mcpServers":{"agentmesh":{"type":"http",
    "url":"${AGENTMESH_URL:-http://127.0.0.1:8787}/mcp",
    "headers":{"Authorization":"Bearer ${AGENTMESH_TOKEN}"}}}}
  ```
  `AGENTMESH_TOKEN` = admin token, provided only for this repo (gitignored `.envrc` via direnv, or a wrapper `AGENTMESH_TOKEN=$(cat <admin-token-file>) claude`) — not exported shell-wide.
- **Constraint: no user-scope install.** Never `claude mcp add -s user`, never edit `~/.claude.json` or `~/.claude/`. To enqueue from another repo's session, use the CLI (`agent-mesh task add`) or open a session in the agent-mesh repo.
- CLI client: URL from `$AGENTMESH_URL` (default `http://127.0.0.1:8787`); token from `$AGENTMESH_TOKEN` else `<config-dir>/admin.token`.

## Architecture
```
Claude session in agent-mesh repo --MCP(.mcp.json)+admin bearer--> hub /mcp --> mcptools --> store
terminal (any dir)                --REST+admin bearer------------> hub /api/v1 --> store
```
- `mcptools.NewHandler(st)` → `mcp.NewStreamableHTTPHandler(func(*http.Request)*mcp.Server{return srv}, &mcp.StreamableHTTPOptions{Stateless: true})` (Stateless verified in go-sdk v1.8.0 by red team) so hub restart strands no sessions. Mounted by phase-1 server behind admin auth.
- go-sdk applies localhost DNS-rebinding protection by default: verify it accepts `Host: 127.0.0.1:8787` and later the tailnet name; configure allowed hosts if needed.
- `morning_report` tool: `st.ListForReport(ctx, since)` + `st.ListHosts` → `report.Build` (stub until phase 4). No phase-4 file touched.

Tool input sketch:
```go
type TaskAddIn struct {
  Repo           string  `json:"repo" jsonschema:"repo key known to hosts"`
  Prompt         string  `json:"prompt"`
  BaseBranch     string  `json:"base_branch,omitempty"`
  BudgetUSD      float64 `json:"budget_usd"`
  PermissionMode string  `json:"permission_mode,omitempty" jsonschema:"acceptEdits|dontAsk|plan"`
  ToolProfile    string  `json:"tool_profile,omitempty" jsonschema:"host-defined profile name, default 'default'"`
  Host           string  `json:"host,omitempty"`
  MaxAttempts    int     `json:"max_attempts,omitempty"`
}
```

## Related Code Files
Own: `internal/mcptools/handler.go` (replace stub), `internal/mcptools/tools_task.go`, `internal/mcptools/tools_hosts_report.go`, `internal/mcptools/format.go`, `internal/mcptools/*_test.go`, `internal/cli/run.go` (replace stub), `internal/cli/task_cmds.go`, `internal/cli/hosts_cmd.go`, `internal/cli/*_test.go`, `.mcp.json`.
Must NOT touch: `cmd/`, `internal/{store,api,hub,client,config,version,hubtest,daemon,report}`, `go.mod`. Missing store method → message lead.

## Implementation Steps
1. **Verify `.mcp.json` expansion first** (15 min): hub from phase 1 with a 1-tool hello server; in repo with token set only via `.envrc`/wrapper, start `claude` → `/mcp` shows agentmesh connected; hub log shows expanded bearer and URL; unset `AGENTMESH_URL` → default used. If `${VAR:-default}` in `url` unsupported → hardcode `http://127.0.0.1:8787/mcp` and document editing locally (keep header `${AGENTMESH_TOKEN}`). If header expansion unsupported → stop, report to lead (no user-scope fallback allowed).
2. Implement tools + formatter; errors → `CallToolResult{IsError:true}` one line.
3. CLI with `flag.FlagSet` per subcommand.
4. Tests.
5. Manual: restart hub mid-session, call tool again (stateless should just work).

## Success Criteria
- [ ] MCP tests via `mcp.NewInMemoryTransports` against store on temp SQLite: add→list→get→cancel→requeue; `bypassPermissions` / `agentmesh/*` base / unknown fields → IsError.
- [ ] HTTP test via `hubtest`: Streamable HTTP client without token → 401; with host token → 403; admin → OK.
- [ ] CLI tests: `cli.Run` against `hubtest`; `--prompt-file -` reads stdin; list output stable.
- [ ] Manual: session in agent-mesh repo adds a task; `agent-mesh task list` shows it. `~/.claude.json` and `~/.claude/` unchanged (`git diff`-style checksum before/after).
- [ ] `tools/list` response < ~1.5 KB.

## Risk Assessment
| Risk | L×I | Mitigation |
|---|---|---|
| `${VAR}` not expanded in `.mcp.json` | M×M | step 1; url fallback hardcoded loopback; header failure → escalate (no user-scope) |
| Admin token leaks via shell env into unrelated sessions | M×M | repo-scoped `.envrc`/wrapper only; `.envrc` gitignored |
| Prompt-injected interactive session enqueues tasks | L×M | tasks run only with host-defined profiles/modes (phase 3); admin reviews via report |
| go-sdk DNS-rebinding check rejects tailnet Host | M×L | configure allowed hosts; test in phase 5 |
| Rollback | — | revert phase commits; stubs return; REST unaffected |
