---
phase: 4
title: "Morning report"
status: pending
priority: P2
effort: "2h"
dependencies: [1]
---

# Phase 4: Morning report

## Overview

One markdown log for "what happened overnight": per host, task, branch, PR, cost, stuck items with reason + resume hint, and why idle hosts were idle. Pure render over store rows; reused by REST `GET /api/v1/report` (phase 1), MCP `morning_report` (phase 2), CLI `agent-mesh report` (here).

## Requirements
- `report.Build(in Input) string` — frozen signature (phase 1); pure, deterministic, no `time.Now()`.
  `Input{Now, Since time.Time; Tasks []api.Task (from store.ListForReport); Hosts []api.Host}`.
- Sections:
  1. Header: window, totals (done/stuck/running/queued/cancelled), total cost (note: client-side estimate).
  2. **Needs attention**: stuck (`#id | repo | host | attempts | reason | session_id | worktree` + `cd <wt> && claude --resume <session_id>`); running with expired lease ("lease expired Xm ago on <host>"); done with `publish_note` = `leftover_commit` or `pr_failed`; cancelled-with-PR.
  3. Done: `#id | repo | host | branch | PR | cost | attempts | note`.
  4. Running: `#id | host | lease expires in`.
  5. Queued: count per repo (no rows).
  6. Hosts, one line each: `api.Host.Summary()` (e.g. `<host-name>: 8c/8GB → 1 slot, throttled: cpu 85%`) + last seen ago, cpu model, mem avail, agent worktrees, running, claude + agent version (skew visible).
- CLI: `agent-mesh report [--since 16h] [--out path]`; REST fetch with admin token. `--out`: refuse if target dir is inside a git work tree (`git -C <dir> rev-parse --is-inside-work-tree`), write 0600, parents 0700.
- Reasons arrive redacted (phase 3); still escape `|`/newlines, truncate 200 chars; done table capped 50 rows + "N more".

## Architecture
```
store.ListForReport + ListHosts ──> report.Build ──> markdown
  ↑ phase-1 REST handler / phase-2 MCP tool      ↑ this phase
agent-mesh report ──REST(admin)──> /api/v1/report?since=16h
```

## Related Code Files
Own: `internal/report/build.go` (replace stub), `internal/report/sections.go`, `internal/report/format.go`, `internal/report/run.go` (replace stub), `internal/report/build_test.go`, `internal/report/run_test.go`, `internal/report/testdata/*.golden.md`.
Must NOT touch: hub, mcptools, store, api. Missing Input field → message lead.

## Implementation Steps
1. Confirm stub `Input` shape; keep signature.
2. Sections + helpers (durations, money, escaping, truncation).
3. `run.go`: flags, client call, safe `--out`.
4. Golden tests with `-update`.

## Success Criteria
- [ ] Golden: fixed Input (done w/ PR, done no_changes, done leftover_commit, stuck at cap, stuck secret-scan, running expired lease, queued ×3, host unhealthy + host 8c/8GB 1 slot throttled cpu 85% + host with slots override) → `testdata/overnight.golden.md`.
- [ ] Empty input → short "nothing ran", no empty tables.
- [ ] Escaping: reason with `|` + newline → one row.
- [ ] `--out` inside a temp git repo → error; outside → file mode 0600.
- [ ] Manual: after phase 3 e2e, report lists task with PR link and cost.

## Risk Assessment
| Risk | L×I | Mitigation |
|---|---|---|
| Report long → tokens via MCP | M×L | no queued rows, truncation, 50-row cap |
| Report with paths/session ids committed to public repo | M×M | `--out` refuses git work trees; 0600 |
| Cost read as bill | L×L | header note |
| Rollback | — | revert; stub returns minimal markdown |
