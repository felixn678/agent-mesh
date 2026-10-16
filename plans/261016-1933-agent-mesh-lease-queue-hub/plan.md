---
title: "AgentMesh: lease queue hub + pull daemons"
description: "Go hub (SQLite fenced lease queue, REST + project-scoped MCP) and per-host pull daemons running one isolated claude -p per task, with journaled PR publish and morning report."
status: pending
priority: P2
effort: 32h
branch: "main"
tags: [go, sqlite, mcp, claude-code, lease-queue, daemon, hooks, messaging]
blockedBy: []
blocks: []
created: "2026-10-16T12:39:40.877Z"
createdBy: "ck:plan"
source: skill
---

# AgentMesh: lease queue hub + pull daemons

## Overview

Stop duplicate/lost overnight work across hosts. Hub owns all state (SQLite). Each host's daemon pulls tasks only when healthy and under load thresholds, runs one isolated `claude -p` per task in a worktree of its own clone, publishes branch + draft PR through a write-ahead journal only while its lease fence is valid, reports once. Interactive Claude sessions on every host (any repo, opt-in `agent-mesh setup-claude`) see agentmesh MCP tools and exchange messages through a hub inbox delivered by hooks. v1 = Ubuntu-only (hub + local daemon on loopback); Mac joins later over Tailscale (phase 3b). Repo is public: plan uses placeholders for all infra.

```
 any interactive Claude session (user-scope MCP + hooks, host token) ─┐   ┌─ terminal: agent-mesh task|hosts|report
 Claude session in agent-mesh repo ─.mcp.json(admin token)────────────┤   │
                                                                      ▼   ▼
             ┌──────── hub (127.0.0.1:8787 v1 · tailnet IP later) ────────┐
             │ REST /api/v1 + MCP /mcp · admin + per-host tokens · reaper │
             │ SQLite WAL synchronous=FULL · tasks hosts sessions messages│
             └──────▲──────────────────────────────────────▲─────────────┘
   claim(caps)/load/│heartbeat/complete/fail/release       │ (pull only, host token)
        daemon (Ubuntu, v1)                          daemon (Mac, phase 3b, via Tailscale)
        healthy & under load? → claim → own clone worktree → claude -p (profile, deny floor,
        strict MCP, project settings) → scan → journal → push → supersede → draft PR → complete
```

## Constraints and non-goals
- **Do not break the user's ClaudeKit workflow.** Never modifies ClaudeKit skills, rules, or existing hooks. Only the **opt-in** `agent-mesh setup-claude` (phase 2) touches user scope: `claude mcp add -s user agentmesh …` (host token, never written in plaintext if avoidable) and **additive** agentmesh hook entries in `~/.claude/settings.json` (backup `settings.json.agentmesh-bak`, idempotent, no removal/reorder of existing entries, `--uninstall` removes only its own). `install.sh` never touches `~/.claude*`. This repo's `.mcp.json` (admin token) stays; in this repo it shadows the user-scope entry of the same name.
- Workers (`claude -p`) unchanged: `--strict-mcp-config`, no user hooks; hook CLI also no-ops under `AGENTMESH_HOOKS=off` (set by daemon).
- Daemons use their own bare clones under `<state_dir>/repos/`; never touch the user's working clones.
- No remote exec, no SSH in code (SSH = human tool), no TLS code, no mDNS code, no tmux.
- Messaging delivery: at turn end (Stop block) or next prompt always; **idle wake** via a background waiter (`agent-mesh inbox --wait`, started with `run_in_background`; Claude re-invokes the session when it exits) — mandatory phase-2 spike. If the spike fails, limitation stands: idle sessions receive only at next prompt/turn end. No Remote Control.
- Out of scope: Claude Code Remote Control, Stop-hook task chaining, hub HA, backup timer, task priority, task size labels (light/heavy → slot weight; later option), group/broadcast messages.

## Key decisions
| Decision | Source |
|---|---|
| Go single binary `agent-mesh`; `cmd/` + `internal/` | user |
| SQLite via `modernc.org/sqlite`, WAL, busy_timeout, synchronous=FULL, AUTOINCREMENT, 1 conn | research §2; red team #13 |
| Single-statement claim with fence; claim filters host caps (repo, mode, profile, budget) | research §3; red team #7 |
| Idempotent complete/fail/release; 409 codes `lease_lost` / `cancelled` | red team #4 |
| Reaper: stuck at cap; requeue (fence+1) leases stale > 1 h; heartbeat revival ≤ 30 min | red team #13 |
| Hub listens first, then grace only for leases alive at `last_alive_at`, once per downtime | red team #11 |
| PULL dispatch; load reported every tick with throttle reason; circuit breaker | user; red team #7, #8 |
| Slots auto-derived from hardware: `clamp(min(cores/4, (ram_gb-8)/4), 1, 4)`, config `max_slots` overrides; cpu%/free-RAM checked before each claim; hardware shown in hosts/report | user |
| Tasks name a `tool_profile`; host maps to allow list + fixed deny floor; no bypassPermissions/auto | red team #1 |
| Workers: `--strict-mcp-config`, `--setting-sources project` (opt-in user), base `agentmesh/*` refused, sensitive-path publish block | user decision; red team #6 |
| Daemon pushes + opens draft PR via journaled, fence-checked steps; supersedes older fences; force-with-lease | phase 3; red team #5, #9, #13 |
| No auto-add of untracked files; gitleaks on diff + PR body; templated PR body; 0700/0600 state | red team #3 |
| Transport: loopback v1; Tailscale for multi-host (manual user install) | user decision; red team #2 |
| Per-host tokens + admin token; hub derives host from token; body/timeout/list limits | red team #14 |
| Host token scope: lease/load + sessions/messages + task add + read-only (list/get/hosts/report); admin only: cancel/requeue | user decision C |
| Opt-in user-scope MCP + additive hooks via `agent-mesh setup-claude` (reverses "no user scope") | user decision A |
| Session messaging via hub inbox: hooks register sessions, inject unread at UserPromptSubmit, block Stop once if unread; messages rendered as untrusted data; fail open | user decision B |
| Idle wake: background `agent-mesh inbox --wait` long-polls hub (≤50 s/req, in-memory notify, no held SQLite conn), exits on message → session wakes | coordinator 2026-10-16; spike-gated |
| GH_TOKEN fine-grained PAT via 0600 env file; headless credential go/no-go gate | red team #12 |
| Official MCP go-sdk v1.8.0, Streamable HTTP, Stateless | research §1, §6; red-team spike |
| gopsutil v4 (darwin no-cgo build verified) | research §4; red-team spike |

## Phases
| # | Phase | Deps | Effort | Status |
|---|---|---|---|---|
| 1 | [Hub core and lease queue](./phase-01-hub-core-and-lease-queue.md) (+ sessions/messages) | — | 8h | Pending |
| 2 | [MCP and CLI task tools](./phase-02-mcp-and-cli-task-tools.md) (+ session tools, hook CLI, setup-claude) | 1 | 6h | Pending |
| 3 | [Host daemon and claude runner](./phase-03-host-daemon-and-claude-runner.md) (3a Ubuntu 10h; 3b Mac optional 3h) | 1 (3b: 5 Tailscale steps) | 13h | Pending |
| 4 | [Morning report](./phase-04-morning-report.md) | 1 | 2h | Pending |
| 5 | [Install and ops](./phase-05-install-and-ops.md) | 2, 3a, 4 | 3h | Pending |

### Parallel execution
After phase 1 merges, phases 2/3/4 run in **3 parallel Claude sessions** (separate worktrees). Phase 1 freezes contracts (stubs, `store.ListForReport`, `hub.NewServer`, `store.New` with clock, `internal/version`, `internal/hubtest`); no shared file edited.

| Phase | Owns exclusively |
|---|---|
| 1 | `cmd/`, `internal/{api,store,hub,client,config,version,hubtest}`, `.gitignore`, initial `go.mod/go.sum` |
| 2 | `internal/mcptools/`, `internal/cli/` (incl. hook + setup-claude), `.mcp.json` |
| 3 | `internal/daemon/`, `go.mod/go.sum` (adds gopsutil only) |
| 4 | `internal/report/` |
| 5 | `Makefile`, `deploy/`, `README.md`, `docs/` |

Frozen contract changes need lead approval + note here. Merge order 4 → 2 → 3, then `go mod tidy && go test -race ./...`.

## Acceptance criteria
- [ ] 200-task / 16-claimer race test: each task claimed exactly once (`go test -race`, real SQLite file).
- [ ] Task enqueued via repo-scoped MCP or CLI runs on an eligible healthy host → draft PR on `agentmesh/task-<id>-f<fence>`, commit made by claude, PR body templated.
- [ ] Lease-lost worker never pushes; replayed complete never closes a good PR; daemon crash after PR create never yields a second open PR (integration tests).
- [ ] Task spec cannot grant tools or bypass permissions; worker cannot push, use gh/curl, or load agentmesh MCP / user hooks (tests + step-2 capture).
- [ ] Secret planted in worktree → publish refused (gitleaks).
- [ ] Hub restart mid-run: claude continues, result lands, no reclaim. Ubuntu reboot without login: hub + daemon healthy.
- [ ] `agent-mesh report` shows host, task, branch, PR, cost, stuck reason, host line e.g. `<host-name>: 8c/8GB → 1 slot, throttled: cpu 85%`.
- [ ] Slot formula unit test: 8c/8GB→1, 8c/16GB→2, 12c/36GB→3, 16c/32GB→4.
- [ ] Session in `~/Code/<repo>` (e.g. lumio) on host A: `sessions` lists a session on host B; `send(<B prefix>, text)` → B receives it, framed as untrusted, at its turn end (Stop block) or next prompt; if B is idle with its waiter running, B wakes within ~5 s (spike-verified). Hub down → every hook exits 0, no output, < 1 s.
- [ ] `setup-claude` on temp HOME with ClaudeKit-like settings fixture: only agentmesh entries added, rerun = no change, `--uninstall` → byte-identical to original; workers still load no agentmesh MCP/hooks.
- [ ] No tokens, private IPs, or real host/user names in repo; without `setup-claude`, `~/.claude/`, `~/.claude.json`, user clones untouched.

## Red Team Review
Sources: [assumption destroyer](./reports/from-code-reviewer-to-planner-red-team-assumption-destroyer-plan-review-report.md), [failure-mode analyst](./reports/from-code-reviewer-to-planner-red-team-failure-mode-analyst-plan-review-report.md), [security adversary](./reports/from-code-reviewer-to-planner-red-team-security-adversary-plan-review-report.md). User accepted all 15 consolidated findings.

| # | Finding | Severity | Disposition | Applied to |
|---|---|---|---|---|
| 1 | Task-supplied `allowed_tools` / tmux shell string / bypass modes → shell on hosts | Critical | Accept | P1 schema+validate (`tool_profile`, mode enum); P2 tool input; P3 profiles, deny floor, tmux removed, config refuses bypass/auto |
| 2 | `.local` resolves to docker IP; plaintext rogue hub | Critical | Accept (user: Tailscale) | P1 listen default loopback; P2 `.mcp.json` url; P3 hub_url; P5 Tailscale manual steps, `.local` removed |
| 3 | Secrets published via auto-add, PR body, logs, `--out` | Critical | Accept | P3 publish s0 (no untracked add, gitleaks, sensitive paths), templated body, redaction, 0700/0600, Read deny floor, env stripping; P4 `--out` guard |
| 4 | Non-idempotent complete; replay closes good PR | Critical | Accept | P1 idempotent ops + 409 codes; P3 s6 handling |
| 5 | Side effects before journal → duplicate PRs | Critical | Accept | P3 write-ahead journal, PR lookup, supersede, per-call timeouts, heartbeat s3, close retried |
| 6 | Workers load MCP + user hooks; agent-written hooks reused | High | Accept (user decision) | P2 no user scope (superseded for interactive sessions by decision A, 2026-10-16; workers unchanged); P3 strict MCP, setting-sources, base refusal, sensitive-path block; P1 base validation |
| 7 | Poison host / first-poller veto burns queue | High | Accept | P1 claim caps SQL + release; P3 release, breaker, `claude auth status` preflight |
| 8 | Worktree leak self-DoS; throttled host invisible | High | Accept | P1 `/hosts/load` + throttle_reason; P3 agent-only count, GC, log retention; P4 hosts section |
| 9 | Git lock contention on human clone; undefined push failure | High | Accept | P3 daemon bare clones, gc.auto=0, push under lock without -u, retry→fail, ls-remote preflight |
| 10 | Orphan claude after daemon crash | High | Accept | P3 run records, orphan sweep + release, SIGTERM release; P5 KillMode=mixed |
| 11 | Boot: bind fails, grace re-extends dead leases | High | Accept | P1 listen-then-grace, last_alive_at/last_grace_at, bind retry; P5 RestartSec/StartLimit, linger, autoconnect step, reboot test |
| 12 | gh/git creds locked in keyring headless | High | Accept | P3 gate incl. push+PR, gh.env GH_TOKEN, `gh auth token` preflight; P5 secrets |
| 13 | Fence/id durability; stale leases never recovered | High | Accept | P1 FULL, AUTOINCREMENT, stale requeue, revival cap; P3 force-with-lease |
| 14 | Shared token, client-asserted host, no HTTP limits, unvalidated refs | Medium | Accept | P1 admin + per-host tokens, host from token, MaxBytes/timeouts/list cap, ref validation, pr_url check; P3 `--end-of-options` |
| 15 | Incomplete frozen contract; scope/estimate | Medium | Accept | P1 freezes ListForReport/NewServer/clock/version/hubtest; priority + backup timer deferred; P3 split 3a/3b; effort 28h |

### Whole-Plan Consistency Sweep
Re-read plan.md + all 5 phases after edits. Removed or replaced:
- tmux mode (P3 runner, config, risks) → removed; peek = `tail -f` log.
- Free-form `allowed_tools` (P1 schema, P2 tool input, P3 argv) → `tool_profile` everywhere.
- `.local` hostname and LAN IP defaults (P2, P3, P5) → loopback v1 / `<hub-tailnet-name>`; ufw /24 rule dropped.
- `synchronous(NORMAL)` → FULL; `INTEGER PRIMARY KEY` → AUTOINCREMENT.
- Single shared bearer token → admin + per-host tokens; body `host` field removed.
- `git add -A` leftovers, raw summary PR body, prompt-derived PR title → removed.
- `git push -u` → `--force-with-lease=…:` without `-u`; human working clone paths in `[repos]` → remote URLs + daemon clones.
- `max_worktrees` incl. human ones → `max_agent_worktrees`.
- `max_concurrent` + `load1/ncpu` → hardware-derived `slots` (override `max_slots`) + `cpu_pct` (later user requirement; P1 hosts schema/HostLoad, P2 hosts line, P3 hardware.go, P4 hosts section, P5 examples).
- "hosts updated only on claim/heartbeat" → load every tick.
- Spool "on network error only" → write-ahead journal.
- `priority` column/ordering, backup timer → deferred.
- user-scope `claude mcp add` → forbidden (constraint); reversed 2026-10-16 by decision A (opt-in `setup-claude` only).
- `UNVERIFIED` tags for go-sdk Stateless/in-memory transport, modernc DSN, gopsutil darwin, `CLAUDE_CODE_OAUTH_TOKEN` → resolved by red-team spike.
- Real usernames, hostnames, IPs, home paths → placeholders.
- 20h estimate → 28h → 32h (messaging + setup-claude).

## Change log
- 2026-10-16: user decisions A (opt-in user-scope MCP + additive hooks via `setup-claude`), B (cross-host session messaging via hub inbox + hooks), C (host token for interactive sessions: messaging + task_add; admin for cancel/requeue). P1 +1h, P2 +3h. Same day: idle wake via background long-poll waiter (spike-gated).

## Open questions
1. ~~Go module path~~ → `github.com/felixn678/agent-mesh` (matches origin).
2. `.mcp.json` `${VAR:-default}` in url — verified in phase 2 step 1 (fallback: hardcoded loopback).
3. Exact behavior of `--setting-sources project` (does it still load user CLAUDE.md/skills?) and Read deny path-rule syntax — phase 3 step 2.
4. Mac LaunchAgent credentials — phase 3b gate.
5. Hub host when Ubuntu off: none in v1.
6. Per-attempt event history table — skipped v1; `reason` keeps last cause.
7. Should `--restricted` replace profile-based isolation once its `--tools` scoping is tested?
8. `plans/handoff-from-lead-session.md` and `plans/reports/researcher-*` contain real infra details (IP, hostname, username) — scrub or keep out of the public repo before first push (outside this plan's file scope).
9. ~~Host token in bypassPermissions sessions~~ → lead decision 2026-10-16: separate per-host **session token** (host identity; sessions, messages, task add, reads; no lease ops, no cancel/requeue). Idle-wake waiter accepted (+1-2h).
10. `SessionAliveWindow` in code is 10 min; idle sessions only touch `last_seen` on prompt/stop → plan recommends 12 h (phase 1).
11. Wake waiter adds ~1–2h not in the 32h budget; accept or trim (e.g. drop Stop waiter-reminder)?

## Addendum: interactive session workers
Interactive sessions can claim tasks (same fence/lease semantics, holder `session:<id>`, 30 min TTL) in addition to daemons. Added: normalized repo identity (`api.NormalizeRepo`, sessions carry `repo`), task `target_host` / `target_session` / `created_by_session`, `POST /sessions/{id}/claim` plus per-session heartbeat/complete/fail/release routes (session token), a hub-to-creator completion message (SQLite trigger on done/stuck), and `claimable=1` on the inbox long poll so idle sessions wake for work. Daemons never take `target_session` tasks and honour `target_host`. Schema v2 upgrades v1 in place. Contract details: see the "Interactive session workers" section of the phase-01 implementation report.
