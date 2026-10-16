# System architecture

## Components

| Component | Role |
|---|---|
| hub (`agent-mesh hub`) | single owner of state. SQLite (WAL, `synchronous=FULL`, one connection). REST `/api/v1`, MCP `/mcp` (Streamable HTTP, stateless), reaper |
| daemon (`agent-mesh daemon`) | per host, pull only. Samples load, claims a task when healthy, runs one `claude -p` worker, publishes, reports |
| CLI (`task`, `hosts`, `report`, `token`) | human surface over REST |
| hooks and waiter (`hook`, `inbox --wait`) | register interactive sessions, deliver inbox messages |
| `setup-claude` | opt-in user-scope MCP and additive hooks |

## Data flow

Task: client (CLI or MCP `task_add`) -> hub queue -> daemon claim (host capabilities sent; hub filters on repo, permission mode, profile, budget) -> worktree from the daemon's own bare clone -> `claude -p` -> heartbeats -> publish -> `complete` -> report.

Messages: session hooks register the session on start and refresh on prompt. `send` stores a message in the recipient's inbox. Delivery: `UserPromptSubmit` injection, one `Stop` block per stop chain, or the idle waiter's long-poll (up to 50 s per request) which exits on arrival and wakes the session.

## Lease, fence, idempotency

- A claim is one SQL statement that sets owner, expiry, and increments the fence. Per-attempt resources are named by fence (branch `agentmesh/task-<id>-f<fence>`).
- Heartbeats extend the lease. `complete`, `fail`, `release` carry the fence; a replay with the same fence is idempotent; a stale fence returns 409 `lease_lost` and a cancelled task 409 `cancelled`.
- The daemon checks the fence before each side effect and never pushes after losing the lease.
- Reaper: expired leases requeue (fence+1) until attempts reach the cap, then the task is stuck with a reason. Leases stale over 1 h are requeued; a heartbeat revives within 30 min.
- After a hub restart the hub listens first, then grants grace only to leases alive at `last_alive_at`, once per downtime.

## Publish journal

Steps are written to a journal before they run: scan (gitleaks on diff), push with `--force-with-lease`, PR lookup by branch, draft PR create, older fences superseded, `complete`. After a crash the daemon replays the journal; PR lookup prevents a second open PR. Untracked files are never auto-added; PR bodies are templated.

## Failure modes

| Failure | Behaviour |
|---|---|
| hub down | daemons back off 2-30 s; running workers continue; results land on return; hooks exit 0 silently |
| daemon crash | orphan workers swept on restart and leases released; otherwise reaper requeues |
| host unhealthy (auth, git, claude) | circuit breaker stops claiming, preflight retried every 10 min |
| network late at boot | hub bind retry, `RestartSec=10`, no start limit |
| token stolen | host/session tokens cannot cancel or requeue; admin separate; rotate by replacing the hash |
| DB loss | manual backup only (see deployment guide) |

## Token model

admin (all), host (daemon: lease, load, reads), session (interactive: sessions, messages, task add, reads). The hub stores SHA-256 hashes, compares in constant time, and derives the host name from the token. Request bodies, timeouts, and list sizes are capped.

## Security

Messages are untrusted data: recipients may run `bypassPermissions`, so rendering prefixes every body line and strips control characters. Workers are launched with strict MCP config and project-only settings, a fixed deny floor (no push, `gh`, `curl`, web fetch, reads of `~/.ssh`, `~/.config`, `~/.claude`), and hooks disabled (`AGENTMESH_HOOKS=off`). Transport is loopback in v1; multi-host only over Tailscale.

## File layout

```
cmd/agent-mesh/     entry point, token command
internal/api        shared types and validation
internal/store      SQLite store: tasks, leases, hosts, sessions, messages
internal/hub        HTTP server, auth, reaper, config
internal/client     REST client
internal/mcptools   MCP tools
internal/cli        human subcommands, hooks, setup-claude
internal/daemon     pull loop, runner, publish journal
internal/report     morning report renderer
deploy/             units, plist, example configs, install.sh
```
