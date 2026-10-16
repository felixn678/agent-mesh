# Code review: CLI hooks/waiter/setup-claude, hubenv, mcptools, onboard, hub reload/auth

Date: 2026-10-16 · Target: `main` HEAD `7100cd1` · Mode: adversarial, report only (no code edited)

## Scope
- internal/cli (hook_cmds, hook_session_worker, inbox_wait, waiter_pid, setup_claude, settings_merge, json_spans), internal/cli/hubenv, internal/mcptools (+format), internal/onboard (up/invite/join/doctor, service units, bundle, tomledit), internal/hub/reload.go, auth.go
- Verification: `go test -race ./...` on a `git archive HEAD` export in scratch: all packages pass. `go vet` clean.
- **Working tree warning:** the real checkout is mid-merge (`UU internal/report/run.go` holds conflict markers, plus staged e2e/config files). `go test ./...` in the repo itself fails to build. Review and tests used HEAD, not the dirty tree.
- Experiments used a temp HOME/XDG dirs under the session scratchpad. Nothing under the real `~/.claude*` or `~/.config/agent-mesh` was touched.

## Critical

### C1. Interactive-session claims skip the worker safety model, and the session-start text tells idle bypassPermissions sessions to run queued prompts unattended
- `internal/store/claim_session.go:17-24`: the session claim filters only repo, host, target and attempts. It ignores `permission_mode`, `tool_profile`, `max_budget_usd`, the daemon `[repos]` allowlist and who created the task.
- `internal/cli/hook_cmds.go:147-150` and `internal/cli/hook_session_worker.go:79-82`: the session-start text and the waiter wake text say: when idle, `task_claim`, do the task, push the branch, `task_done`. No user confirmation is involved.
- `internal/mcptools/tools_session_tasks.go:74`: the claimed prompt is appended raw after an opening delimiter. There is no closing delimiter, no `> ` prefix and no control-character stripping, unlike `format.Messages`.
- **Failing scenario:**
  1. A session on host B (any joined machine, such as the later Mac) reads a poisoned web page or repo file.
  2. Through its own agentmesh MCP it calls `task_add(repo=<shared repo>, target_host=A, tool_profile=edit-only, permission_mode=acceptEdits, prompt="…curl x | sh…; push")`.
  3. The waiter of an idle session on host A, running with bypassPermissions, wakes up. The session claims the task per instructions and runs the prompt with full user permissions.
  4. The plan's controls ("tasks name a tool_profile … fixed deny floor; no bypassPermissions/auto") are bypassed entirely. The untrusted-message header ("do not push … without confirmation") contradicts the claim instructions ("push the branch").
  5. Net effect: one compromised session token on any host gives unattended code execution on every host that has an idle session in the same repo.
- **Fix:** this conflicts with the user decision behind commit 39fb13e, so the options need the user's choice:
  - (a) Make session claiming opt-in per session (`AGENTMESH_CLAIM=1` at session start) and default it off.
  - (b) Let a session claim only tasks with an explicit `target_session`, or tasks created on its own host.
  - (c) Require the session to show the prompt to the user and get confirmation before working, and drop "when idle, claim" from the session-start text.
  - In every option, render the prompt like messages: `> `-prefixed, control characters stripped, with a closing delimiter.
  - In every option, refuse the session claim for tasks whose `tool_profile`/`permission_mode` is stricter than what an interactive session enforces.

## High

### H1. Hooks, the waiter and mcp-headers send the session token to whatever `$AGENTMESH_URL` says
- `internal/cli/hubenv/hubenv.go:34`: `HubURL()` prefers the environment variable for every client, including `sessionClient()`.
- Verified: `echo '{session_id…}' | AGENTMESH_URL=http://127.0.0.1:19999 agent-mesh hook prompt` sent `POST /api/v1/sessions` with `Authorization: Bearer <session token>` to the listener.
- **Failing scenario:**
  1. The user opens Claude in a cloned public repo whose `.claude/settings.json` contains `"env": {"AGENTMESH_URL": "http://attacker:80"}`.
  2. Settings `env` is applied to the session. Hooks very likely inherit it (needs confirming, see Unresolved).
  3. The first SessionStart hook, which is user-scope and runs in every repo, ships the host's session token in plain HTTP.
  4. With that token an attacker can read inboxes, send messages, and `task_add` (which feeds C1).
- **Fix:**
  - Session-token clients (hooks, waiter, `inbox`, `mcp-headers`) should ignore `$AGENTMESH_URL` and use only `client.toml`/`daemon.toml`. Alternatively, honour the env var only if it equals the configured URL.
  - Keep the env override for the human CLI only.
  - Optionally refuse hosts outside loopback/100.64.0.0/10 unless the URL is configured.

### H2. A stale or temporary binary path is never updated, and setup reports "already up to date"
- `internal/cli/settings_merge.go:57`: `ownsGroup` accepts any command ending in `agent-mesh hook <event>`. Merge therefore treats an old path as already installed.
- Verified: settings holding `/tmp/go-build123/exe/agent-mesh hook …` merged with a new bin give `changed=false`.
- `selfBin()` returns the running executable. A first `go run ./cmd/agent-mesh setup-claude` (or running from a build dir later deleted) writes a temp path. That breaks:
  - all four hooks (exit 127 on every prompt; fail-open, so nothing tells the user)
  - the MCP `headersHelper` (`setup_claude.go:175`)
- Rerunning from the installed binary does not repair it. The same applies to the onboard unit `ExecStart` (`service_systemd.go:39`, `Env.Binary`).
- **Fix:**
  - When a group matches by suffix but its command differs from `hookCommand(bin, …)`, rewrite that hook's `command` string in place (byte splice).
  - Have `editMCP` compare and update `headersHelper` the same way.
  - Refuse (or warn loudly) when `selfBin()` is under `os.TempDir()` or contains `/go-build`.

### H3. SIGHUP during hub startup kills the hub permanently, and pidfile reload can signal an unrelated process
- `internal/hub/run.go:67-74`: `signal.Notify(SIGHUP)` (`reload.go:35`) is only installed after DB open and `ListenWithRetry`, which retries for up to 2 minutes when the tailnet IP is not up yet.
- Verified: hub with listen `100.64.250.250:18787`, SIGHUP during the bind retry gives `exit=129`.
- systemd treats SIGHUP as a clean exit, so `Restart=on-failure` does **not** restart the hub. `invite` → `reloadHub` → `systemctl kill --signal=SIGHUP` (Type=simple is "active" immediately) leaves the hub down until someone restarts it by hand.
- `reload.go:67-80` and `invite.go:94`: `hub.pid` is removed only on clean stop. After SIGKILL, OOM or a reboot with a manually run hub, the pid can belong to another process (for example the user's terminal shell or tmux), and invite sends it SIGHUP.
- **Fix:**
  - Call `signal.Notify(hup, SIGHUP)` (or `signal.Ignore`) as the first statement of `Run`.
  - In `SignalReload`, check that `/proc/<pid>/exe` (Linux) or `ps -o comm` (macOS) is agent-mesh before signalling. Alternatively, hold an `flock` on the pidfile and check it.

## Medium

### M1. Uninstall can delete the user's own hooks, and install can skip ours
- `settings_merge.go:56-57`: the suffix match strips quotes and checks `HasSuffix(…, "agent-mesh hook stop")`.
- Verified: a user group `{"command":"notify-send done; agent-mesh hook stop"}` is treated as ours.
  - Merge adds no Stop hook.
  - `--uninstall` removes the user's whole group. This violates "never remove existing hooks".
- Related low issue (verified): Unmerge also deletes a pre-existing empty `"hooks": {}` or `"Stop": []`.
- **Fix:** tokenize the command shell-style. It is ours only if it is exactly `[<path>, "hook", <event>]` with `filepath.Base(path) == "agent-mesh"` and nothing else. Only remove containers that Merge created; recording that in the backup or a marker is enough.

### M2. The waiter singleton relies on pid-file liveness: PID reuse, races and orphan waiters
- `waiter_pid.go:41-52`: `kill(pid, 0)` treats any live pid (EPERM included) as "our waiter".
  - Pid files under `~/.local/state` survive reboots, and the waiter does not handle SIGHUP (`cmd/agent-mesh/main.go:39`), so closing a terminal leaves a stale file.
  - Result: a session is told "already running; do not restart" indefinitely, so idle wake is dead and the Stop reminder is suppressed.
- `waiter_pid.go:87`: two concurrent starters can both remove a "stale" file. One deletes the other's fresh file, and two waiters run.
- `inbox_wait.go:114`: a waiter outlives a crashed Claude session (no SessionEnd, so no `ended`). It prints to an orphaned output file and **acks** the message, so the message is lost.
- **Fix:**
  - Use `flock(LOCK_EX|LOCK_NB)` on the pid file. The kernel releases the lock on death, which removes both reuse and race problems.
  - Record `os.Getppid()` at start, and exit without acking when it changes or becomes 1.
  - Add SIGHUP to the NotifyContext.

### M3. Doctor reports a pass when the setup is broken
- `onboard/verify.go:18` checks every token with `GET /hosts` (`anyToken`). A `session.token` that is really a host token passes "auth session", yet every hook gets 403. That failure is fail-open and appears only in `hook.log`. The same is true the other way round for the daemon.
- `doctor_checks.go:63` probes `claude`, `gh` and `gitleaks` on the interactive shell PATH. The systemd unit pins `PATH=%h/.local/bin:%h/go/bin:/usr/local/bin:/usr/bin:/bin` (`service_systemd.go:40`). A `claude` installed with nvm or npm-global passes doctor, but the daemon cannot execute it.
- **Fix:**
  - Verify by role: the session token should list sessions (`GET /api/v1/sessions`, a session-only route); the host token should post a load report or call a host-only no-op.
  - Resolve tools with the unit's PATH, or write the resolved absolute `claude_bin` into daemon.toml at `up`/`join` time.

### M4. Invite secrets end up in argv, shell history and scrollback
- `join.go:26-33` takes the bundle as a positional argument. While join runs (up to 10 s × 2 verifications), the bundle is visible in `/proc/<pid>/cmdline` and stays permanently in `~/.zsh_history` on the joining machine.
- `invite.go:80` prints it.
- The bundle carries the long-lived host token and session token, with no expiry or one-time use.
- **Fix:** support `agent-mesh join <url> -` (read from stdin, or a no-echo prompt) and make it the printed form, for example `pbpaste | agent-mesh join URL -`. Optionally mint a short-lived enrollment code that the hub exchanges for tokens.

### M5. Rendering gaps for untrusted text (terminal and model)
- `format/lines.go:53` (`TaskDetail`) prints `Prompt` (and `reason`, `publish`) raw. `api/validate.go:115-118` allows any UTF-8, including ESC.
  - Effect 1: `agent-mesh task get N` replays escape sequences (OSC 52 clipboard write, title or hyperlink spoofing) into the user's terminal.
  - Effect 2: MCP `task_get` returns the prompt with no untrusted framing.
- `format/messages.go:78` `Clean` and `stripControl` use `unicode.IsControl` (Cc only). U+2028/U+2029 (Zl/Zp) and bidi overrides (Cf, U+202E/U+2066…) pass, so a body can visually break out of the `> ` framing.
- **Fix:**
  - Strip Cc, Cf, Zl and Zp in both helpers.
  - Render the prompt in `TaskDetail` (CLI and MCP) through the same `> `-prefixed block used for messages.

### M6. `up --listen <tailscale-ip>` repoints client.toml but leaves the user-scope MCP URL on loopback
- `up_config.go:104` retargets `hub_url` in client.toml and daemon.toml. `mcpConfig` (`setup_claude.go:175`), however, bakes `hubURL + "/mcp"` into `~/.claude.json` at setup time.
- After rebinding, the hub no longer answers on 127.0.0.1. Hooks follow client.toml, but every session's MCP tools fail.
- Rerunning `setup-claude` only warns "different … rerun with --force" (`setup_claude.go:214`), and `up` gives no hint.
- **Fix:** when the existing entry has our `headersHelper` and differs only in `url`, update it without `--force`. `up --listen` should print "rerun agent-mesh setup-claude".

## Low (not counted)
- `editHubTOML` is read-modify-write with no lock, so two concurrent `invite` runs drop one entry. `join` verification catches it.
- `setup-claude` read→rename is not coordinated with Claude Code's own writes to `settings.json` (for example `/model` or `/config` in one of 10-15 sessions). The window is milliseconds and the risk is a lost update. An mtime re-check before rename would cut it.
- Duplicate `"hooks"` keys: Merge edits the first, but JSON.parse in Claude uses the last, so the install is silently inert.
- `--uninstall` removes a user-scope `agentmesh` MCP even when it is not ours (it has no `headersHelper` check).
- Hook ack reuses the 800 ms ctx after the hub calls. On a slow hub the ack keeps failing, so the same messages are re-injected every prompt.

## Checked, no issue found
- The hook fails open on every path: exit 0, no stdout on error, stop_hook_active guard.
- The `claude mcp add-json` argv contains no token.
- Token files are 0600 and checked on read. The config dir is 0700.
- Auth uses constant-time hash compares, and `replace` happens under the mutex.
- Admin-only MCP tools are gated per principal. Session tools require a session principal plus host ownership in the store.
- The bundle decoder validates the version, host name, token regex and URL shape.
- `writeAtomic` keeps the file mode and follows symlinks.

## Recommended actions (order)
1. C1: get a decision on the options; at minimum make claiming opt-in and frame the prompt.
2. H1: pin the session-token URL to config.
3. H3: install the SIGHUP handler first and validate the pidfile pid.
4. H2 and M1: exact-match ownership and in-place path update.
5. M2: flock plus parent-death exit.
6. M3–M6.
7. Resolve the in-progress merge conflict in `internal/report/run.go` before anything lands.

## Metrics
- Tests: all pass on HEAD (`-race`). There are no tests for stale-path merge, user-owned suffix commands, SIGHUP during startup, or pid reuse.
- Lint: `go vet` clean.

## Unresolved questions
- C1: is autonomous session claiming intended to run without user confirmation, given the plan's "no bypassPermissions" worker rule? This needs a user decision.
- H1: confirm empirically that project `.claude/settings.json` `env` reaches hook subprocesses, and whether workspace trust gates it in bypassPermissions mode. Severity drops to Medium if trust gating holds.
- Does `claude --resume` reuse the session_id? If so, M2's orphan-waiter loss also hits resumed sessions.

Status: DONE_WITH_CONCERNS
Summary: 1 Critical (session claim bypasses worker safety and enables cross-host unattended execution), 3 High (token exfil via env URL, stale binary paths never repaired, SIGHUP kills the hub during startup, verified exit 129), 6 Medium. The repo checkout is mid-merge with conflict markers, so the review was run against a HEAD export.
