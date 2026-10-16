# Live session test: hooks, idle wake, session workers

Status: DONE_WITH_CONCERNS (all 6 checks observed live; 1 delivery bug found and fixed; 1 path only partly live)

## Setup
- Claude Code 2.1.301, `--model haiku`, 2 interactive sessions in detached tmux panes:
  `claude --settings <tmp>/settings.json --mcp-config <tmp>/mcp-<x>.json --strict-mcp-config --setting-sources project,local --permission-mode bypassPermissions`.
- Real HOME for auth only. User settings, hooks, skills and `~/.claude.json` mcpServers untouched (hooks + MCP passed by flags; Claude itself wrote transcripts and a trust entry per temp repo).
- agent-mesh fully isolated: `HOME`/`XDG_CONFIG_HOME`/`XDG_STATE_HOME` scratch dirs per identity. Hub via `agent-mesh up --no-service --listen 127.0.0.1:<port> --name ha`; second identity via `invite hb` + `join --no-service` into its own config dir.
- Session A ("alpha", host ha) in repoA with origin `https://github.com/<org>/live-demo.git`; session B ("beta", host hb) in repoB with origin `git@github.com:<org>/live-demo.git` (same project, different URL forms).
- MCP config: http + literal session-token header in a 0600 scratch file.
- tmux note: `send-keys Enter` only inserts a newline in the Claude input; `C-m` submits.

## Results
| # | Check | Result | Evidence |
|---|---|---|---|
| 1 | SessionStart registers | PASS | `GET /sessions?alive=1` right after launch: `host=ha name=alpha repo=github.com/<org>/live-demo` (both URL forms normalized to the same key) |
| 2 | Session starts waiter from additionalContext | PASS | First prompt "follow the agentmesh instructions": both sessions ran `agent-mesh inbox --wait --session <id>` with `run_in_background`; pid files present. Context is only seen once the session takes a turn: a never-prompted session has no waiter |
| 3 | A `send` → idle B wakes by itself | PASS | B woke ~15 s after the send (incl. model time), replied "pong"; A (idle) then woke on the reply. After the fix below: send → B reply in **3.0 s** |
| 4 | A `task_add target_host=hb` → B claims, does it, `task_done` → A notified | PASS | 21:01:39 queued, 21:01:51 `running session:<B>`, 21:02:03 `done` with summary; repoB has commit "Add hello.txt" on branch `live-hello`; hub auto-message reached A, whose waiter woke it and it summarized the result. ~24 s end to end |
| 5 | Stop hook block-once | PASS (reminder path) / unit-tested (message path) | Transcript: Stop with dead waiter, nothing unread → `hook_blocking_error` with restart reminder; model restarted waiter; next Stop allowed (no block). A message-carrying Stop block was not reproduced live (the prompt hook delivered it first, see finding 1) |
| 6 | SessionEnd on `/exit` | PASS | `/exit` first asks "Background work is running → Exit and stop tasks"; after confirm, hub shows beta `alive=false ended=true`; waiter process gone, pid file removed |

## Findings
1. **Bug (fixed): waiter acked messages the model never saw.** The waiter printed messages to its background output file and acked them. B's model was busy, never read the file, and message 5 was lost (read_at set, body absent from transcript). Also observed: when a background command exits, Claude queues a `<task-notification>` turn and **runs UserPromptSubmit hooks for it**, so hook context reliably reaches the model.
   Fix: the waiter only announces (`N unread message(s) from host/id8 ...`) and never acks. The wake-up turn's prompt hook injects and acks. `<id>.announced` high-water mark keeps an unacked (hook failed) message from waking the session in a loop; while only announced messages or cooling-down work are pending the waiter pauses `cooldownPoll` (10 s) between polls instead of spinning. Re-verified live: send → B woke, hook injected (`hook_additional_context UserPromptSubmit`), B replied "8", both messages acked, 3.0 s.
2. **Silent signal exit confused the model.** After the harness killed B's waiter, an empty exit-0 output led haiku to restart it with `nohup … &` (no wake notifications; it missed the next message until re-prompted). The waiter now prints `waiter stopped by a signal (no message); restart it with run_in_background …`.
3. Base branch defaulted to `main`; repo had `master`. Worker branched from master and said so in the summary (task spec default; not changed).
4. `/exit` with a running waiter shows a confirmation dialog (exit and stop tasks / move to background / stay).

## Code changes on test/live-sessions
- `internal/cli/inbox_wait.go`: announce-only waiter, announced high-water mark, cooldown pause, signal notice; tests updated (no ack, no re-wake for announced id, prompt hook delivers + acks, newer message wakes).
- `internal/hubtest/hubtest.go`: `boot` waits for `/healthz` (fixes startup-grace race on early Stop).
- `internal/cli/hubenv`: `HubURLIn(dir) (url, source)`; `internal/onboard/doctor_checks.go` `hubURLFor` uses it (one resolver).
- README: delivery paragraph.

`go vet ./...` clean; `go test -race -count=2 ./...` all ok.

## Not verified live
- Stop `decision:block` carrying messages (unit-tested only).
- Two physical hosts (both identities on one machine, loopback).
- Waiter behavior when Claude itself is killed without `/exit` (orphaned waiter; review item M2 addresses parent-death exit).

## Unresolved questions
- Should SessionStart also tell the session to start the waiter immediately (a hook cannot run it; the model only acts on its first turn)?

## Final re-verification on main (bee064d), 2026-10-16 21:30

Status: DONE, all 6 checks passed live; no code changes needed.

Setup: same isolation (scratch `HOME`/`XDG_*` per identity; real HOME only for Claude auth; no change to the user's `~/.claude*` config). `up --no-service` hub + `invite hb` / `join` (code pasted on stdin). Hook and MCP files were produced by the **real `agent-mesh setup-claude`** on each identity's temp HOME (real `claude mcp add-json` wrote the temp `~/.claude.json`), then passed unchanged via `--settings` / `--mcp-config --strict-mcp-config --setting-sources project,local`: managed commands `<bin> hook <event> --managed-by=agent-mesh`, MCP `headersHelper` = `<bin> hook mcp-headers --managed-by=agent-mesh` (no token in any file). Sessions (haiku, bypassPermissions): alpha (host ha, repoA `https://…/live-final.git`), beta (host hb, repoB `git@…:…/live-final.git`), gamma (host hb, repoC `ssh://git@…/live-final`, `AGENTMESH_WORKER=off`).

| # | Check | Result | Evidence |
|---|---|---|---|
| 1 | Managed hooks + headersHelper as users get them | PASS | All 3 sessions registered with repo `github.com/<org>/live-final`; alpha's MCP `task_add` calls authenticated through `headersHelper` under `--mcp-config` |
| 2a | Idle wake on message | PASS | REST send alpha→beta; idle beta woke, prompt hook injected, replied "7": **6.0 s** send→reply; both messages acked |
| 2b | Idle wake on claimable task, end to end | PASS | task #1 `target_host=hb` → beta claimed (`session:<beta>`), committed hello.txt on `live-hello` (84586bb), `task_done` summary; hub notice "[agent-mesh] task #1 done" delivered and acked to alpha |
| 3 | `AGENTMESH_WORKER=off` | PASS | `hosts --sessions` shows `0502ce6c hb repoC [gamma] worker:off`; gamma stayed idle (no wake, no claim) although task #1 matched its host and repo; repoC untouched |
| 4 | `task_add` replay | PASS | Two identical calls with `request_id=live-final-1`: `queued #1 …` then `already queued (same request_id) #1 …`; one task row |
| 5 | `/exit` cleanup | PASS | "Exit and stop tasks" → beta `ended=true`; waiter gone; flock on `<id>.pid` held before, free after |
| 6 | `kill -9` claude (no /exit) | PASS | waiter (ppid = Claude's background shell, grandparent = claude) exited **0.3 s** after the kill; lock free. The shell's SIGHUP likely ended it before the 2 s parent-watch tick (parent watch is unit-tested). Session stays registered until the alive window lapses (no SessionEnd) |

Notes: hooks register a session ~1-2 s after the TUI appears (an immediate check saw 0 sessions). Invite output now prints the code on its own line for the stdin prompt.
