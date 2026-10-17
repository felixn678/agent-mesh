# System architecture

## Components

| Component | Role |
|---|---|
| hub (`agent-mesh hub`) | single owner of state. SQLite (WAL, `synchronous=FULL`, one connection). REST `/api/v1`, MCP `/mcp` (Streamable HTTP, stateless), reaper, token reload on `SIGHUP` |
| daemon (`agent-mesh daemon`) | per host, pull only. Samples load, claims a task when healthy, runs one `claude -p` worker per task, publishes, reports |
| interactive sessions | ordinary Claude Code sessions with the agentmesh MCP tools and hooks: add tasks, message each other, claim tasks for their own repo when idle |
| hooks and waiter (`hook`, `inbox --wait`) | register sessions (with their normalized git remote), deliver inbox messages, wake idle sessions for messages or claimable work, extend a held lease at Stop |
| CLI (`task`, `hosts`, `host-note`, `report`, `token`) | human surface over REST |
| onboarding (`up`, `invite`, `join`, `doctor`) | config and token files, `hub.toml` edits, invite bundles, user services, health checks |
| `setup-claude` | opt-in user-scope MCP server and additive hooks |

## Data flow

Task: client (CLI or MCP `task_add`) -> hub queue -> taker claims -> work -> `complete` / `fail` -> requester session messaged (if the task has `from`) -> report.

Takers:
- **Daemon**: sends its capabilities (repo keys, permission modes, tool profiles, budget cap) and a request id; the hub hands out the oldest queued task that fits, respects `host` / `target_host`, and never gives it a task with a `target_session`. Worktree from the daemon's own clone -> `claude -p` -> heartbeats -> journaled publish.
- **Idle session**: `task_claim` hands out the oldest queued task whose repo equals the session's repo and whose `host` / `target_host` / `target_session` allow this host and session. The session works on a branch, pushes, and calls `task_done`.

Routing between machines: `host_profiles` holds each machine's role note (set with `host-note`: a session token for its own machine, admin for any) and the OS and tools its session-start hook detected (file checks only, no subprocess). `GET /api/v1/hosts/profiles` merges profiles, daemon-reported hosts and machines with live sessions. With two or more machines the session-start hook appends a briefing that lists them and tells the session to set `target_host` when a request clearly fits another machine better, unless the user named one. The hub itself never routes by notes; it only enforces `target_host`.

Repo keys are compared in canonical form (`api.CanonicalRepo`): git remotes become `github.com/<user>/<repo>`, short keys pass through.

Messages: session hooks register the session on start and refresh it on prompt. `send` stores a message in the recipient's inbox. Delivery: `UserPromptSubmit` injection, one `Stop` block per stop chain, or the idle waiter's long-poll (up to 50 s per request), which exits on arrival or on claimable work and so wakes the session. Clients fetch without marking read and ack only what they showed (at least once).

## Lease, fence, idempotency

- A claim is one SQL statement that sets owner, expiry, and increments the fence. Per-attempt resources are named by fence (branch `agentmesh/task-<id>-f<fence>`). A retried claim with the same request id returns the same task and fence instead of a second one.
- Heartbeats extend the lease (TTL 5 min). `complete`, `fail`, `release` carry the fence; a replay with the same fence is idempotent; a stale fence returns 409 `lease_lost` and a cancelled task 409 `cancelled`. `release` gives the task back without using an attempt.
- An expired lease can be taken by the next claim (fence+1). A heartbeat revives an expired lease that nobody took within the revive window (30 min).
- Reaper (every minute): a task at its attempt cap whose lease expired past the revive window becomes stuck with a reason; the owner may still record a late `complete` or `fail`. Leases stale over 1 h are requeued (fence+1). Queued tasks whose `target_session` ended or has been unseen for the session alive window (or never registered, once the task is that old) become stuck with reason `target session gone`; the creator gets the usual notice and an admin may requeue.
- Task add takes an optional `request_id`: a retry with the same id from the same principal (`created_by`) returns the existing task (200 instead of 201); reusing the id for a different repo or prompt is a 409.
- After a hub restart the hub listens first, then grants grace only to leases alive at `last_alive_at`, once per downtime.

## Daemon

- Slots from hardware: `clamp(min(cores/4, (ram_gb-8)/4), 1, 4)`, overridable with `max_slots`. Before each claim: healthy, running < slots, CPU below `max_cpu_pct`, available memory above `min_mem_avail_pct`, agent worktrees below `max_agent_worktrees`. The first failing check is reported to the hub as `throttle_reason` on every poll.
- Preflight at start and every 10 min while unhealthy: `git`, `claude --version`, `claude auth status`, `gh auth token`, `git ls-remote` per repo, `gitleaks version`. Two infrastructure failures in a row (clone/fetch/worktree, or claude exiting without a result) open the circuit breaker: no claims until preflight passes.
- Clones live in `<state_dir>/repos/` (bare, `gc.auto=0`, fetched per base branch). All ref changes on a clone run under a per-repo lock. Repository hooks are disabled for daemon git calls.
- Worker: `claude -p --output-format stream-json` with the prompt on stdin, in its own process group, with `--strict-mcp-config`, `--setting-sources project`, `--permission-prompts none`, `--tools` limited to the profile's tool names, `--allowedTools` = profile, `--disallowedTools` = fixed deny floor (`git push`, `gh`, `curl`, `wget`, WebFetch, WebSearch, reading or editing `~/.ssh`, `~/.config`, `~/.claude`, `~/.claude.json`, git config and credential files, shell rc files, `~/.local/bin`, LaunchAgents, the agent-mesh config dir wherever the OS puts it (e.g. `~/Library/Application Support/agent-mesh`) and the dir holding `daemon.toml`, the token and env files named in `daemon.toml`, the hub database in the state dir, and the daemon's repos/spool/run/logs dirs; paths with spaces verified against claude 2.1.301). Env: GitHub and hub tokens and every `AGENTMESH_*` variable removed (then `AGENTMESH_HOOKS=off`), ssh agent only for `allow_ssh_agent` profiles. Commands the daemon runs inside a worktree pin `GIT_DIR` to the daemon-owned admin dir. Run records let a restarted daemon kill orphaned workers and release their tasks.
- Lease lost while the worker runs: kill the process group, publish nothing. Lease lost after it finished: nothing is pushed, but the daemon still tries to record the outcome (accepted if the task was only reaped to stuck).
- Claims back off 5 s to 5 min (with jitter) while the hub is unreachable; heartbeat errors are logged and the worker keeps running; reports are retried until they land.

## Publish journal

Each step is written (fsynced) to `<state_dir>/spool/task-<id>-f<fence>.json` before it runs, and every step is safe to repeat:

1. prepare: refuse untracked files; commit leftover tracked changes; no commits -> complete with `no_changes`; refuse changes to `.claude/`, `.mcp.json`, `CLAUDE.md`, `.github/workflows/`, gitleaks config; gitleaks on the new commits.
2. fence check, then push with `--force-with-lease=<branch>:` (branch must not exist yet; no `-u`).
3. fence check, then close open PRs of older attempts (lower fence) of the same task.
4. reuse an open PR for the branch, else `gh pr create --draft` with a templated body (ids, cost, commit subjects; never the model's text), itself scanned by gitleaks.
5. `complete`. On 409 `lease_lost` the PR is closed (retried until it succeeds); an idempotent replay or a cancel leaves it open.

After a crash the daemon replays the journal; the PR lookup prevents a second open PR. Refused publishes leave the task stuck with the worktree kept for a human (`claude --resume <session_id>` inside it).

## Failure modes

| Failure | Behaviour |
|---|---|
| hub down | daemons back off 5 s-5 min; running workers continue; results land on return; hooks exit 0 silently |
| daemon crash | orphan workers killed on restart and their tasks released; pending publishes replayed from the journal |
| host unhealthy (auth, git, claude) | circuit breaker stops claiming, preflight retried every 10 min |
| network late at boot | hub bind retry (2 min), `RestartSec=10`, no start limit |
| token stolen | host/session tokens cannot cancel or requeue; admin separate; rotate by replacing the hash and sending `SIGHUP` |
| DB loss | manual backup only (see deployment guide) |

## Token model

admin (all), host (daemon: lease, load, reads), session (interactive: sessions, messages, task add, session claims, reads). The hub stores SHA-256 hashes, compares in constant time, and derives the host name from the token. Request bodies, timeouts, and list sizes are capped.

## Security

Messages are untrusted data: recipients may run `bypassPermissions`, so rendering prefixes every body line and strips control characters. Daemon workers are isolated as described above. Transport is loopback in v1; multi-host only over Tailscale.

## File layout

See [codebase-summary.md](codebase-summary.md).
