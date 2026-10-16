---
phase: 3
title: "Host daemon and claude runner"
status: pending
priority: P1
effort: "13h (3a Ubuntu 10h + 3b Mac optional 3h)"
dependencies: [1]
---

# Phase 3: Host daemon and claude runner

## Overview

`agent-mesh daemon` per host. Pull loop: sample load → report load every tick → if healthy and under thresholds, claim one task (caps sent, hub filters) → worktree from a **daemon-owned clone** → one `claude -p` per task, isolated (host tool profile, deny floor, no MCP, no user hooks) → heartbeat → journaled, fence-checked publish (scan, push, draft PR) → report once. No remote exec, no SSH, no tmux. **3a = Ubuntu (v1 ship). 3b = Mac (optional, needs Tailscale + auth gate).**

Context: [dispatch report](../reports/researcher-261016-1933-claude-dispatch-hooks-report.md) §1, §2, §5 · [go-hub-stack report](../reports/researcher-261016-1933-go-hub-stack-report.md) §3, §4 · red-team reports in [reports/](./reports/)

## Constraints
- Never touches `~/.claude/` or the user's working clones. Clones live in `<state_dir>/repos/`.
- Workers: `--strict-mcp-config` (no agentmesh or other MCP), `--setting-sources project` (no user/global hooks) unless host opts in.
- Hub never decides tools: task names a `tool_profile`; host maps it.

## Requirements

### Config `~/.config/agent-mesh/daemon.toml` (placeholders)
```toml
hub_url = "http://127.0.0.1:8787"       # v1 Ubuntu-local; later http://<hub-tailnet-name>:8787
host_token_file = "~/.config/agent-mesh/host.token"
state_dir = "~/.local/share/agent-mesh"  # repos/ worktrees/ logs/ spool/ run/ — dirs 0700, files 0600
poll_interval = "30s"; heartbeat_interval = "60s"; task_timeout = "3h"   # wall clock
git_call_timeout = "120s"                # < lease_ttl/2
# max_slots = 2                          # optional override; default auto-derived from hardware (below)
max_cpu_pct = 80; min_mem_avail_pct = 25 # checked before every claim
max_agent_worktrees = 6                  # counts only <state_dir>/worktrees/*
stuck_retention = "72h"; log_retention = "168h"
allowed_permission_modes = ["acceptEdits", "dontAsk"]   # bypassPermissions/auto → daemon refuses to start
max_budget_usd_cap = 5.0
worker_setting_sources = "project"       # opt-in "user,project" to load user hooks
claude_bin = "claude"
claude_env_file = ""                     # 0600; e.g. CLAUDE_CODE_OAUTH_TOKEN=… (env name verified in binary)
gh_env_file = "~/.config/agent-mesh/gh.env"   # 0600; GH_TOKEN=<fine-grained PAT>; passed to git/gh only
gitleaks_bin = "gitleaks"

[repos]                                  # allowlist: task.repo -> remote URL (daemon clones it)
agent-mesh = "https://github.com/<user>/agent-mesh.git"

[tool_profiles.default]
allow = ["Read","Edit","Write","Glob","Grep",
         "Bash(git status*)","Bash(git diff*)","Bash(git log*)","Bash(git add *)","Bash(git commit *)",
         "Bash(go test *)","Bash(go build *)","Bash(go vet *)"]   # repo test/build cmds per host
[tool_profiles.edit-only]
allow = ["Read","Edit","Write","Glob","Grep","Bash(git add *)","Bash(git commit *)"]
```
Deny floor always appended (not configurable): `Bash(git push*)`, `Bash(gh *)`, `Bash(curl *)`, `Bash(wget *)`, `WebFetch`, `Read(~/.ssh/**)`, `Read(~/.config/**)`, `Read(~/.claude/**)`, `Read(<state_dir>/**)` (path-rule syntax verified in step 2).

### Behavior
- Claim sends caps: `repos` keys, `allowed_permission_modes`, `tool_profiles` keys, `max_budget_usd_cap`. Post-claim re-check (defense in depth): mismatch → **release** + skip that id 30 min.
- At most one claim per poll tick. `POST /hosts/load` every tick with `throttle_reason` (`slots_full|load|mem|worktrees|unhealthy:<err>|""`).
- **Circuit breaker**: 2 consecutive infra failures (auth, git fetch/clone, worktree add, claude exit with no result line) → stop claiming, `unhealthy`, re-run preflight every 10 min; pass → resume.
- Preflight (start + breaker): `git --version`, `claude --version`, `claude auth status`, `gh auth token >/dev/null` (with gh.env), `git ls-remote origin HEAD` per repo (with gh.env), `gitleaks version`. Any fail → unhealthy, no claims.
- Base branch re-validated host-side (no `agentmesh/` prefix, ref-format); every git call uses `--end-of-options` before positionals.
- Prompt via stdin. Claude env = os env **minus** `GH_TOKEN`, `GITHUB_TOKEN`, `AGENTMESH_TOKEN` + claude_env_file. git/gh env = os env + gh_env_file.
- Hub down: claim backoff 5 s→5 min + jitter; heartbeat network errors logged, claude keeps running; only 409 acts.

### Verified CLI facts (claude 2.1.301, `claude --help`)
`-p`, `--output-format stream-json`, `--verbose`, `--max-budget-usd`, `--permission-mode`, `--permission-prompts none`, `--allowedTools`, `--disallowedTools`, `--append-system-prompt`, `--strict-mcp-config`, `--setting-sources user,project,local`, `--restricted`, `--tools`, `auth status`, `setup-token`. Absent: `--max-turns`. `CLAUDE_CODE_OAUTH_TOKEN` env name present in binary (red team).

## Architecture

### Files (all owned by this phase)
```
internal/daemon/run.go           replace stub: flags, config, preflight, start loop, signal handling
internal/daemon/config.go        Config + defaults + validation (refuse bypassPermissions/auto, perms 0600)
internal/daemon/preflight.go     checks above; health state
internal/daemon/loop.go          tick: load report, breaker, slot accounting, claim one, backoff
internal/daemon/hardware.go      startup: cores, total RAM, CPU model (gopsutil); DefaultSlots(cores, ramGB) pure func
internal/daemon/load.go          gopsutil: cpu %, mem avail %, agent worktree count
internal/daemon/repo_clone.go    per-repo mutex; bare clone (gc.auto=0, fetch refspec, credential helper); fetch
internal/daemon/worktree.go      worktree add/remove, GC (stuck_retention, log_retention, superseded)
internal/daemon/runner.go        argv build, start (Setpgid), run record, wait, kill pgroup
internal/daemon/claude_result.go parse stream-json → Result{Text,SessionID,CostUSD,IsError,Subtype}
internal/daemon/task_exec.go     per-task goroutine: setup → run → heartbeat → publish → report
internal/daemon/publish.go       journaled steps, scans, push, supersede, PR create/close
internal/daemon/journal.go       spool records: write+fsync, load, replay
internal/daemon/redact.go        secret regex redaction for reasons/stderr tails
internal/daemon/testdata/        fake-claude.sh, fake-gh.sh, result fixtures (from step 2)
```
go.mod/go.sum: adds `github.com/shirou/gopsutil/v4` (only parallel phase touching go.mod).

### Daemon-owned clones
`<state_dir>/repos/<repo>.git`: `git clone --bare <url>`; `git config remote.origin.fetch '+refs/heads/*:refs/remotes/origin/*'`; `gc.auto=0`; `credential.helper '!gh auth git-credential'` (uses GH_TOKEN from gh.env — verify in gate). All fetch / worktree add / worktree remove / push / branch delete / daily `git gc` under per-repo mutex. User's working clones never touched.

### Per-task flow
```
claim(caps) → recheck ──mismatch──> release + skip
  → lock; fetch origin <base>; worktree add -b agentmesh/task-<id>-f<fence> <wt> origin/<base>; unlock
       infra error → release + breaker++
  → write run record run/task-<id>-f<fence>.json {pid:0, fence, wt}; start claude; update {pid, pgid, started_at}
  → heartbeat/60s:  409 lease_lost|cancelled → kill pgroup (TERM, KILL+30s), remove worktree, delete run record, STOP
                    net err → continue
  → wall-clock timeout → kill → fail(retry=false, "timeout")
  → exit: parse log
       no result line → fail(retry=true, redact(stderr tail 500B)); breaker++
       is_error / budget → fail(retry=false, subtype) → stuck (worktree kept stuck_retention)
       ok → publish
```

### Publish (journaled; record fsynced **before** each side effect)
Journal `spool/task-<id>-f<fence>.json` = `{id, fence, repo, wt, branch, base, result, step, pr_url, outcome}`.
```
s0 prepare   untracked files (git status --porcelain '??') → fail(retry=false, "untracked files left") ; keep wt
             tracked uncommitted → git commit -a (publish_note=leftover_commit)
             0 commits ahead of origin/<base> → complete(publish_note=no_changes) ; done
             diff touches .claude/ .mcp.json CLAUDE.md .github/workflows/ → fail(retry=false, "sensitive path: …")
             gitleaks git --log-opts=origin/<base>..HEAD on wt → finding → fail(retry=false, "secret scan hit") 
s1 heartbeat (fence check)                       409 → cleanup, STOP (no push)
s2 journal step=pushing → under repo lock:
             git push --force-with-lease=refs/heads/<branch>: origin HEAD:refs/heads/<branch>   (no -u, no config write)
             transient error → retry ×3 backoff → fail(retry=true, "push: …")
             lease rejected (branch exists) → fail(retry=false, "branch collision")
   journal step=pushed
s3 heartbeat                                     409 → journal step=done_lost; STOP (branch harmless, unique)
s4 supersede: gh pr list --state open --json number,headRefName → close PRs with head agentmesh/task-<id>-f<k>, k<fence>
s5 journal step=pr_creating → existing open PR for <branch>? reuse : gh pr create --draft --base <base> --head <branch>
             --title "agent-mesh task #<id>" --body <template>   ; journal step=pr_created, pr_url
             gh error → pr_url="" publish_note=pr_failed
             body template: task id, host, fence, attempts, cost, session_id, commit subjects; **no model text**;
             template output piped through `gitleaks stdin` before use
s6 journal step=completing → complete(fence, branch, pr_url, session_id, cost, publish_note)
             200 / already_applied → s7
             409 lease_lost → journal step=closing_pr → gh pr close <url> --comment "superseded by newer attempt" (retried from journal until ok)
             409 cancelled → leave PR open (human decides), report shows it
             net err → keep journal, retry with backoff
s7 worktree remove; branch -D (under lock); delete journal + run record
```
Every git/gh call: `context.WithTimeout(git_call_timeout)`. Wall-clock jump > 2×heartbeat_interval detected (wall vs monotonic delta) → force heartbeat before next step.
Replay on daemon start: for each journal resume at `step` (pr_creating → look up PR by head branch first; idempotent complete makes s6 safe).

### Who pushes / opens PR
Daemon, after fence-checked heartbeats (s1, s3); claude only commits (push denied by floor). Branch `task-<id>-f<fence>` + AUTOINCREMENT ids + `--force-with-lease=…:` → a zombie push cannot overwrite another attempt. Hub-opens-PR rejected: hub would need gh rights for every repo and no worktree. Residual race (reclaim between s3 and s6) → idempotent complete distinguishes replay from lease loss; only `lease_lost` closes the PR; newer attempt's s4 also closes older-fence PRs.

### Orphans and shutdown
- Daemon start: for each `run/*.json` → if pid alive and cmdline matches claude → kill(-pgid); then `release(id, fence, "daemon restart")` (409 ignored); remove worktree unless a journal exists (journal replay owns it).
- SIGTERM: stop claiming; kill all pgroups; release each (fence); exit within `TimeoutStopSec` 60 s. Journals in-flight are kept for replay.

### Capacity (hardware-derived slots)
- At startup: `cores = cpu.Counts(true)`, `ram_gb = round(mem.VirtualMemory().Total / 2^30)`, `cpu_model = cpu.Info()[0].ModelName` (fallback "unknown"; truncated 100 chars).
- `DefaultSlots(cores, ramGB) = clamp(min(cores/4, (ramGB-8)/4), 1, 4)` (integer division; reserves ~8 GB for OS + human work, ~4 GB + 4 cores per claude task). Config `max_slots` overrides (`slots_overridden=true`).
- Reported every tick in `HostLoad` (cores, ram_gb, cpu_model, slots, override) → hosts table → CLI/report line e.g. `<host-name>: 8c/8GB → 1 slot, throttled: cpu 85%`.

### Load, worktrees, GC
- `cpu.Percent(0, false)` (delta since previous tick), `mem.VirtualMemory().Available/Total` (human load counted here); worktrees = entries in `<state_dir>/worktrees/*` only.
- Claim iff healthy && running<slots && cpu_pct<max_cpu_pct && mem_avail>min && agent_worktrees<max. First failing check → `throttle_reason` (`slots_full`, `cpu 85%`, `mem 12%`, `worktrees`, `unhealthy: …`).
- GC on start + daily: remove worktrees of lease-lost/superseded immediately (already), stuck worktrees older than `stuck_retention`, logs older than `log_retention`; `git worktree prune`; `git gc` per clone under lock.

### Runner argv (no shell strings)
```
claude -p --output-format stream-json --verbose --max-budget-usd <b> --permission-mode <m>
  --permission-prompts none --strict-mcp-config --setting-sources <worker_setting_sources>
  --allowedTools <profile.allow...> --disallowedTools <floor...>
  --append-system-prompt "Unattended agent-mesh task <id>. Work only in this directory. Commit with clear messages. Do not push or open PRs; do not create files outside the repo."
stdin=prompt file (0600); stdout=logs/task-<id>-f<fence>.jsonl; stderr=logs/….err; cwd=wt; Setpgid
```
Peek: `tail -f` the jsonl. Resume by human: `cd <wt> && claude --resume <session_id>` (shown in report for stuck).

## Related Code Files
Create: files above, `go.mod`/`go.sum` (gopsutil). Must NOT touch: `cmd/`, `internal/{store,api,hub,client,config,version,hubtest,mcptools,cli,report}`. Need contract change → ask lead.

## Implementation Steps — 3a Ubuntu
1. **GO/NO-GO gate: unattended credentials on Ubuntu.** Reboot, **do not log in to GUI**; with linger, run in the user-manager context (`systemd-run --user --wait --pipe --property=EnvironmentFile=<gh.env> …`): `claude auth status`; `claude -p "say ok" --output-format json`; `git ls-remote` + push a test branch to a scratch GitHub repo through a bare clone with gh credential helper + GH_TOKEN; `gh pr create --draft` + `gh pr close`. All pass → GO. Fail → fix (setup-token → claude_env_file; PAT scopes) or NO-GO and escalate. Record results here.
2. **CLI behavior capture** (scratch repo): prompt via stdin; `--setting-sources project` skips a marker-writing hook in `~/.claude/settings.json`-equivalent (test with a **temporary `HOME`**, never edit real `~/.claude/`); `--strict-mcp-config` loads no MCP; default profile can `git commit` but `git push` and `Read(~/.ssh/...)` are denied; init line has session_id; last line `type:"result"` with `result, session_id, total_cost_usd, is_error, subtype`; exit codes success / budget (0.01) / SIGTERM. Save trimmed fixtures `testdata/result_*.jsonl`.
3. config + validation; preflight; load; repo_clone; worktree; claude_result; redact.
4. runner, task_exec, journal, publish, loop, run (signals, orphan sweep, replay).
5. Tests; then Ubuntu e2e.

## Implementation Steps — 3b Mac (optional; after Tailscale per phase 5)
1. Gate on Mac as LaunchAgent (user logged in, screen locked): `claude auth status`, `claude -p`, bare clone push + `gh pr create/close` with gh.env. Fallback: `claude setup-token` → `CLAUDE_CODE_OAUTH_TOKEN` in claude_env_file. All fail → Mac stays out of v1.
2. Runtime check gopsutil on darwin (`agent-mesh daemon --print-load`).
3. Verify orphan sweep under launchd (claude in own pgid survives daemon kill → swept on restart).
4. Sleep test: lid close mid-task → wake → wall-clock jump → heartbeat → `lease_lost` or revival per revive_window.
5. Mac config: slots auto-derived (check the computed value in `agent-mesh hosts`; set `max_slots` only if Mac still lags), `max_agent_worktrees=2`, `hub_url` = tailnet name.

## Success Criteria
- [ ] Unit: `DefaultSlots` table — 8c/8GB→1, 8c/16GB→2, 12c/36GB→3, 16c/32GB→4, plus 4c/4GB→1 (floor), 64c/256GB→4 (cap); `max_slots` override wins; threshold table + throttle_reason strings; config refuses bypassPermissions/auto/world-readable env files; argv contains floor + strict-mcp + setting-sources and no shell; branch/path naming; result parser on real fixtures; redaction (ghp_, github_pat_, sk-ant-, Bearer, 32+ hex).
- [ ] Git tests (real git, temp bare origin): bare clone setup; worktree add/remove; `--end-of-options` with `-x` branch rejected; force-with-lease collision → retry=false; untracked file → fail; sensitive path → fail; gitleaks finds planted fake token → fail; concurrent 4 task setups + pushes under `-race`.
- [ ] Integration (`hubtest` + fake-claude.sh + fake-gh.sh):
  - happy path → remote branch exists, done, pr_url, worktree + journal removed, body has no model text.
  - lease lost mid-run (advance clock, other host claims) → pgroup killed (no orphan pid), no push, no complete.
  - complete response dropped (hubtest Restart after commit) → replay gets already_applied → **PR stays open**.
  - complete 409 lease_lost → fake gh gets `pr close`; gh close fails once → retried from journal.
  - daemon killed after `pr_created` → restart replays → no second PR; newer fence closes older fence PR.
  - hub down mid-run → claude continues; hub back (grace) → complete lands.
  - daemon killed mid-claude → restart kills orphan, releases (attempts unchanged).
  - 2 consecutive infra failures → breaker, throttle_reason=unhealthy, no further claims.
  - caps: task needing unknown profile never claimed by this host.
- [ ] Manual Ubuntu e2e: real claude, budget $0.50, trivial prompt → draft PR in scratch repo; claude made the commit (no leftover_commit note); cost matches JSON.
- [ ] 3b (if pursued): Mac gate results recorded; one task completes from LaunchAgent.

## Risk Assessment
| Risk | L×I | Mitigation |
|---|---|---|
| Token holder / injected session gets shell | M×H | host-owned profiles, fixed deny floor, no bypass/auto, no shell strings |
| Secrets published to public repo | M×H | no auto-add of untracked, gitleaks on diff + PR body, templated body, Read deny floor, env stripping |
| Credentials unavailable headless (keyring) | H×H | step-1 gate incl. push+PR; GH_TOKEN env file; claude setup-token fallback |
| Duplicate PRs after crash/replay | M×H | write-ahead journal, PR lookup by head, supersede older fences, idempotent complete |
| Good PR closed on replay | M×H | already_applied 200; only lease_lost closes |
| Poison host burns attempts | M×H | caps in claim SQL, release w/o attempt, circuit breaker, auth preflight |
| Git lock contention / human clone damage | M×M | daemon-owned bare clones, gc.auto=0, all ref ops under per-repo mutex |
| Orphan claude after daemon crash | M×M | run records + sweep on start; SIGTERM release |
| Auto slots wrong (Linux reports RAM below nominal, e.g. 15.4 GiB → 1 slot) | M×L | `round()` GiB; `max_slots` override; computed value visible in hosts/report |
| Task size varies (one heavy task saturates host) | M×M | cpu/mem thresholds before each claim; task size labels (light/heavy) = later option, not v1 |
| Worktree/log leak stops claims | M×M | count agent worktrees only; GC; throttle_reason visible |
| Worker loads user hooks / MCP | M×M | `--setting-sources project`, `--strict-mcp-config`; base `agentmesh/*` refused; sensitive-path publish block |
| `go test` in profile runs repo-controlled code | L×M | repos are user-owned allowlist; documented |
| Stop-hook task chaining | — | NOT v1 (YAGNI); later option per dispatch report §2 |
| Rollback | — | stop daemon unit; revert phase commits; leases expire/requeue; journals left on disk for manual inspection |
