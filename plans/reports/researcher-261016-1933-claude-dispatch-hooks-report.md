# Research: Claude Code headless dispatch, hooks, remote messaging, SSH

Date: 2026-10-16 · Scope: agent-mesh overnight dispatch (Ubuntu + MacBook, LAN, headless)
Sources: official Claude Code docs (code.claude.com/docs/en/…), fetched via summarizer. Primary: `headless`, `hooks`, `mcp`, `remote-control`, `cross-session-messaging`. Exact field names should be confirmed with one local test run.
Not sourced: SSH/macOS/keychain specifics (section 4). Marked as general knowledge.

## 1. Headless vs interactive (`claude -p` vs tmux `claude`)

Verdict: `claude -p` for workers. Interactive-in-tmux only for humans who want to steer.

| Need | `claude -p` | Interactive in tmux |
|---|---|---|
| Completion signal | Exit code: 0 ok, non-zero fail, 143 on SIGTERM ([headless](https://code.claude.com/docs/en/headless)) | Must scrape pane / transcript |
| Final result + cost | `--output-format json`: `result`, `session_id`, `total_cost_usd`, per-model breakdown | None native |
| Resume after crash | `--resume <session_id>` (any cwd on same machine) | `claude --resume` manual |
| Permission control | `--permission-mode {auto,dontAsk,acceptEdits}`, `--allowedTools "Bash(git diff *),Edit,…"`, `--permission-prompts none` (v2.1.259+) | Prompts block the run |
| Spend cap | `--max-budget-usd` (stops run) | None |
| Long-run monitoring | `--output-format stream-json --verbose` | n/a |

Notes:
- `-p` waits for background subagents/commands. Idle wait cap 10 min (`CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS`, `0` = none). Set it for long jobs.
- `--continue`/`--resume` cost totals include earlier runs' spend (client-side estimate).
- Not verified in fetched docs: `--max-turns`, `--worktree`, `is_error`/`subtype` JSON fields. Check `claude --help` locally before relying on them. Use `git worktree` in the wrapper instead of `--worktree` (KISS).
- `--spawn worktree` exists only for `claude remote-control` server mode (section 3), not `-p`.

Overnight recipe: host daemon claims task (hub lease) → `git worktree add` → `tmux new-session -d` running `claude -p … --output-format json --max-budget-usd N --permission-mode dontAsk --allowedTools …` → wrapper writes exit code + JSON to hub → on exit≠0 or timeout, lease released.

## 2. Hooks (`.claude/settings.json`)

Settings scopes: `~/.claude/settings.json` (user), `.claude/settings.json` (project, committable), `.claude/settings.local.json` (project, gitignored). Hook entries merge across levels.

Common stdin: `session_id`, `transcript_path`, `cwd`, `permission_mode`, `hook_event_name`. Exit 0 = success (stdout JSON parsed). Exit 2 = blocking (stderr = reason). Other = non-blocking notice.

**SessionStart**
- Matcher: `startup | resume | clear | compact | fork`.
- Input: `source`, `model` (may be absent), `agent_type`, `session_title`.
- Output: `hookSpecificOutput.additionalContext`, `initialUserMessage` (first user msg in `-p` mode), `sessionTitle`.
- **No blocking control.** Exit 2 only shows stderr to user, Claude doesn't see it.
- Use: register host with hub; inject task context.

**Stop** (the "claim next task" question)
- Stdin adds: `stop_hook_active` (true when already continuing from a stop hook), `last_assistant_message`, `background_tasks` (array), `session_crons` (array).
- Block: stdout `{"decision": "block", "reason": "claim next task from hub: call claim_task"}`. Or exit 2 with reason on stderr.
- `reason` is required when blocking. It is fed to Claude as the instruction to continue.
- **Loop guard is the hook's job**: docs say check `stop_hook_active` yourself. Plus Claude Code cap: 8 consecutive continuations without a tool call, then block overridden. Counter resets on any tool call. Raise via `CLAUDE_CODE_STOP_HOOK_BLOCK_CAP`. A `claim_task` MCP call resets it, so the cap is not hit in a healthy loop.
- Non-error continuation: `hookSpecificOutput.additionalContext` with `hookEventName: "Stop"`.
- Does not run on user interrupts. API errors fire `StopFailure` instead.
- Unverified: whether a Stop-block loop works identically under `-p`. Test it.

**SubagentStop**: same block format. Stdin adds `agent_id`, `agent_type`, `agent_transcript_path`. Reason is delivered to subagent as next instruction.

**SessionEnd**
- Matcher: `clear | resume | logout | prompt_input_exit | other`.
- No decision control. Side effects only.
- Default budget 1.5 s total across hooks; per-hook `timeout` up to 60 s.
- Runs on SIGTERM too, but nothing else runs then. **Do not rely on SessionEnd to release leases.** Lease expiry (heartbeat) is the real mechanism.

**Gotcha**: `-p` runs project `.claude/settings.json` hooks and `.mcp.json` servers even in untrusted folders, with no trust dialog. `--bare` skips all of it, but also skips hooks (so no Stop loop), MCP (unless `--mcp-config`), inbox socket, CLAUDE.md. `--bare` also needs `ANTHROPIC_API_KEY`. OAuth/keychain is not read in bare mode. So Stop-loop workers cannot be `--bare`.

## 3. Project MCP (`.mcp.json`) and Remote Control / cross-session messaging

**`.mcp.json`**
- Project root, `{"mcpServers": {...}}`, committable.
- Interactive: approval prompt before use. Pending servers show `⏸ Pending approval`.
- `-p` / SDK: **no prompt, loads project servers silently.** Keep out with `disabledMcpjsonServers` (blocks all modes) or `--strict-mcp-config` + `--mcp-config <file>` for workers.
- `enableAllProjectMcpServers` / `enabledMcpjsonServers` in a project file are ignored in untrusted folders.
- `-p` cannot complete OAuth. Mesh MCP server must use stdio or a static token, not OAuth.
- `-p` waits up to `MCP_TIMEOUT` (30 s) for pending servers when `--mcp-config` is passed.

**Remote Control**
- Outbound HTTPS only. Local `claude` process must stay running. Laptop sleep = session paused, auto-reconnect.
- Requires claude.ai subscription (Pro/Max/Team/Enterprise). **API keys not supported.** Team/Enterprise owner must enable toggle.
- Transcript (messages, tool activity) **stored on Anthropic servers** while connected. Relevant for private repos.
- Server mode: `claude remote-control --spawn {same-dir|worktree|session} --capacity N` (default 32) with `--permission-mode`. `worktree` gives each session its own git worktree.

**Cross-session messaging (ListAgents/SendMessage)**
- Same machine: per-session Unix socket, never via Anthropic. Needs v2.1.224+ (macOS/Linux).
- Other machine: **through Anthropic servers**, only when both sessions are on Remote Control and the sender is signed in via claude.ai (not API key, not Bedrock/Vertex/Foundry).
- Idle receiver: a delivered message starts a new turn. So yes, an idle RC session is woken.
- Target `offline` (RC dropped): message queued, delivered on reconnect.
- Inbound default for `-p`: messages from peers are **held** for approval, dropped after `dialogExpiry` (5 min default). For unattended workers set `crossSessionInbound: "accept"` in worker `--settings`. Or use `dialogExpiry: "never"`.
- `--bare` sessions do not bind inbox socket: invisible to peers.
- Messages are plain text, ~1M char cap, peer messages can't approve permissions or change config.
- Controls: `crossSessionInbound` (accept|hold|refuse), `isolatePeerMachines`.

Verdict for mesh: **do not use messaging for dispatch or claims.** It needs claude.ai auth on every box, sends transcripts to Anthropic, and drops messages to `-p` workers by default. Hub + leases is simpler and works offline from Anthropic. Keep RC only for human steering from phone/browser.

## 4. SSH dispatch, macOS, tmux, worktrees (general knowledge, NOT verified against docs)

- Forced command: `~/.ssh/authorized_keys` line `command="/usr/local/bin/mesh-dispatch",restrict ssh-ed25519 AAAA… mac`. `restrict` disables pty, port/agent/X11 forwarding, and user rc. `command=` runs that script for any key use. Client's requested command is in `$SSH_ORIGINAL_COMMAND`.
- Script pattern: parse `$SSH_ORIGINAL_COMMAND` into `case` of fixed verbs (`claim`, `run <task-id>`, `status`, `load`). Reject everything else. Never `eval` it. Validate task IDs with a strict regex.
- Hub auth token is still needed (SSH key ≠ per-action authz). Keep it out of git.
- macOS: enable Remote Login (System Settings → General → Sharing), or `sudo systemsetup -setremotelogin on` (may need Full Disk Access for the terminal app). Non-interactive shells get a minimal PATH. Source the profile explicitly in the forced-command script (`zsh -lc`, or set PATH).
- tmux: `tmux new-session -d -s <name> -c <worktree> 'cmd …; echo EXIT:$? > <file>'`. Write exit code to a file so completion is detectable without attaching.
- Worktree: `git worktree add -b mesh/<task> <path> origin/main`. Cleanup: `git worktree remove <path>` then `git worktree prune`. Remove only after branch pushed.
- **Keychain risk (unverified)**: on macOS, Claude OAuth creds sit in the login keychain. Over SSH the keychain may be locked (no GUI session), so `claude -p` may report not logged in. Workarounds: `security unlock-keychain` in the wrapper (password on disk = bad), run dispatch from a logged-in GUI session, or `ANTHROPIC_API_KEY` (but then RC/cross-session messaging unavailable). Test on the Mac before design freeze. Consider this a go/no-go check.

## 5. Token cost guidance (from docs)

- Cost = turns × context. Transport is not the cost.
- `--max-budget-usd` hard-stops a run. Use per worker task.
- `--bare` cuts startup overhead (skips CLAUDE.md, hooks, plugins, MCP, auto memory). Trade-off: loses Stop loop and MCP unless passed in.
- Resume cost: SessionStart on `resume` exposes `prompt_cache_likely_expired` and `estimated_cache_write_usd`. Long gaps between `--resume` calls re-pay cache writes. Prefer one long run per task over many resumes.
- `--permission-prompts none` + `dontAsk` avoid stalled runs (stall = paid idle waiting; no direct doc on idle cost).
- Cost fields are client-side estimates, not the bill. Reconcile with the usage dashboard.
- No docs guidance found on model choice per task (`--model` exists per Remote Control notes). Not researched further.

## Ranked recommendation

1. **Wrapper-driven one-`claude -p`-per-task** (host daemon owns claim/lease; exit code + JSON result to hub). Simplest, testable, survives crashes via `--resume`, no hook dependency. Use for v1.
2. **Stop-hook continuation loop** only if one session must chain tasks. Requires `stop_hook_active` guard, `claim_task` as the loop's tool call, and a test under `-p`. Adds coupling; add later if (1) proves too slow to start.
3. **Remote Control / cross-session messaging**: optional human steering only. Not in the dispatch path.
Transport: SSH forced-command with fixed verbs (section 4). Coordination: own SQLite queue (GitHub Issues later if needed).

Adoption risk: Claude Code CLI changes fast. Many flags gated by v2.1.2xx. Pin the CLI version on each host and record it in the hub's host registration. Hooks/Stop semantics have changed (e.g., `fork` source, 8-continuation cap). Re-verify before implementation.

## Limitations

- Fetched via summarizer; field names and exact behaviors need a local test (`claude -p` with a sample Stop hook; inspect JSON output).
- Did not fetch: full CLI reference (`--max-turns`, `--worktree`), `settings-reference`, `permissions`, `permission-modes`, `sessions`, `agent-view`, `claude-code-on-the-web`.
- No primary source for SSH, macOS Remote Login, tmux, keychain-over-SSH (section 4). Needs a Mac test.
- Did not test any of this on the two machines.
- Budget: ~7 web fetches (over the ~5 guideline) to cover the Stop-hook and messaging gaps.

## Unresolved questions

1. Does a Stop-hook `decision: block` continuation work identically in `claude -p`? (Test.)
2. Does `claude -p` on Mac over SSH see the keychain OAuth login? (Test; go/no-go.)
3. Exact JSON output fields: `is_error`, `subtype`, `num_turns`, `stop_reason` present in `--output-format json`? (Check with local run.)
4. `--max-turns` and `--worktree` on the installed CLI version? (`claude --help`.)
5. Is the Mac's claude.ai login on the same account and plan as Ubuntu's? (Needed only for RC/messaging.)
6. Do overnight `-p` workers need `crossSessionInbound: accept` set in `--settings`, or is hub-only dispatch enough? (Recommend: hub only, so moot.)
7. Acceptable to have worker transcripts on Anthropic servers? Only matters if RC is used. Private-repo policy call for user.
8. Which host runs the hub when Ubuntu is off? (Open from handoff, unchanged.)

Status: DONE_WITH_CONCERNS
Summary: Official docs confirm `claude -p` (exit codes, JSON cost/session fields, permission flags, `--resume`) fits overnight workers better than tmux-interactive. Stop hook can block-and-continue via `{"decision":"block","reason":...}` with `stop_hook_active` guard and 8-continuation cap. Cross-machine messaging needs claude.ai auth and sends via Anthropic, so keep dispatch on own hub+leases.
Concerns: SSH/macOS/keychain section not source-verified; Stop-loop under `-p` untested; field names came via summarizer; web lookups exceeded the ~5 guideline (about 7) to cover Stop and messaging gaps. Mac keychain-over-SSH is a go/no-go test before design freeze.
