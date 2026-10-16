# Handoff: AgentMesh lead session → agent-mesh repo

Date: 2026-10-16 · From the "AgentMesh - lead" session

## Problem
- User runs many Claude sessions (each w/ subagents, many git worktrees) overnight on 2+ machines: Ubuntu + MacBook, same wifi; maybe a 3rd machine later.
- Sessions on different machines can't see each other → work independently, duplicate work.
- Mac lags/hangs from too many parallel worktrees; Ubuntu has spare capacity.

## Decided scope
Messaging between sessions → **leave to Claude Code Remote Control** (built-in; ListAgents/SendMessage list same-account sessions on other machines when connected). Not yet tested cross-machine — test first.

Repo builds only 4 things:
1. **Task queue with leases** — `claim_task` w/ lock; holder heartbeats; lease expiry → task returns to queue (session dies at 3am ≠ lost work). No duplicate claims.
2. **Machine load** — each host reports CPU/RAM/active worktree count; route new work to least-loaded host.
3. **Dispatch over SSH** — create worktree + start `claude` in tmux on target host, headless. Results return as pushed branches/PRs.
4. **Morning report** — one log: host, task, branch, PR, stuck items + reason.

Form factor idea (not final): MCP server in repo (`.mcp.json` auto-loads when `claude` opens in repo) + small hub on Ubuntu (SQLite), discovery via mDNS (avahi on Ubuntu, Bonjour on Mac) w/ static-config fallback. Hooks in `.claude/settings.json`: SessionStart registers host; Stop hook checks inbox/queue so overnight sessions continue.

## Constraints / decisions
- **Token cost:** cost driver = number of turns × session length, not transport. Design for few, complete messages; state lives in queue/files, read on demand; no status ping-pong; workers report only on result.
- **Security:** dispatch = remote exec. Need auth token + allowlisted commands only (no arbitrary shell). Hub binds LAN only; never commit tokens.
- YAGNI/KISS: don't rebuild messaging; keep hub small.
- Possible alt coordination layer: GitHub Issues + labels (`ghpm` skill) — consider vs own SQLite queue during planning.

## Current infra state
- Ubuntu `<hub-host>`, user `<user>`, IP `<hub-lan-ip>`, mDNS `<hub-host>.local` (avahi active, not verified from Mac).
- openssh-server installed + enabled, port 22. Key-only auth planned once both machines exchange keys.
- Ubuntu has an ed25519 SSH key.
- tmux installed on Ubuntu. No tailscale, no mosh.
- **Pending on Mac (user):** ssh-copy-id to Ubuntu, `Host ubuntu` in `~/.ssh/config`, enable Remote Login, add Ubuntu pubkey to Mac `authorized_keys`, note Mac hostname + username locally → then add `Host mac` on Ubuntu + switch to key-only auth.
- Later option: Tailscale for off-LAN / 3+ machines.

## Next step
1. (Optional, recommended) test Remote Control across Ubuntu ↔ Mac.
2. Run `/ck:plan` for agent-mesh using this scope; plans in `plans/<timestamp>-<slug>/`.

## Unresolved questions
- Remote Control cross-machine messaging: works as described? wakes idle sessions?
- Own SQLite queue vs GitHub Issues as task store?
- Language/runtime for hub + MCP (Node likely, matches user's tooling) — decide in plan.
- Which host runs hub when Ubuntu is off?
