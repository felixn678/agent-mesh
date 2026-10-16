# agent-mesh

Coordinate many Claude Code sessions across your machines.

## What it is for

Each machine runs 10-15 interactive Claude sessions across 2-3 projects, and every project is cloned on both machines. With agent-mesh:

- From any session you type a prompt, optionally naming a machine ("run this on `<host-name>`").
- The work is split into tasks and queued on the hub.
- Idle sessions of the same project on the target machine pick tasks up and message the results back.
- Sessions on different machines message each other through the hub inbox (`sessions`, `send`, `inbox`).

A secondary mode runs overnight: a headless daemon per host pulls tasks, runs one isolated `claude -p` worker per task in a git worktree of its own clone, and opens a draft PR. Leases are fenced, so a task is never done twice and a stale worker can never publish. `agent-mesh report` summarises the night.

```
 interactive Claude sessions (MCP + hooks, session token) ─┐   terminal: agent-mesh task|hosts|report
                                                           ▼
                 hub (SQLite WAL, REST /api/v1 + MCP /mcp, 127.0.0.1:8787)
                                                           ▲  pull only, host token
                 daemon per host ──► claude -p in worktree ─┘
```

Design details: [docs/system-architecture.md](docs/system-architecture.md). Install and ops: [docs/deployment-guide.md](docs/deployment-guide.md).

## Non-goals

No remote exec, no SSH in code, no TLS or mDNS code, no tmux, no hub HA, no task priorities, no group messages. Multi-host traffic is plaintext HTTP and is only supported over Tailscale.

## Quick start

Prereqs: git, gh, claude, gitleaks (`agent-mesh doctor` tells you what is missing). Build with `make build` (or `make cross`) and put `agent-mesh` on your PATH.

On the hub machine:

```sh
agent-mesh up                      # config (0700), tokens, hub.toml, daemon.toml, user services
agent-mesh setup-claude            # connect Claude Code sessions here
agent-mesh doctor                  # green checks = ready
```

`up` is idempotent: re-running keeps existing tokens and configs. It listens on `127.0.0.1:8787` and never binds a wildcard address. If Tailscale is present it prints the command to serve other machines (`agent-mesh up --listen <tailscale-ip>:8787`). Flags: `--listen`, `--name <host>` (default: short hostname), `--no-service`. On Linux run `loginctl enable-linger "$USER"` (hinted) so services survive logout and reboot.

Add another machine (yours or a friend's, same tailnet):

```sh
agent-mesh invite <host-name>      # on the hub: prints one line, reloads the hub (SIGHUP, no restart)
agent-mesh join <hub-url> <bundle> # on the new machine: paste that line
agent-mesh setup-claude && agent-mesh doctor
```

The invite line contains the new machine's tokens: send it over a private channel only. `join` verifies the tokens against the hub before writing anything.

Manual setup (tokens, units, editing `hub.toml` by hand) is still supported: see [docs/deployment-guide.md](docs/deployment-guide.md).

CLI commands read the hub URL and token from your config; use the admin token for cancel/requeue and the morning report (see the deployment guide for wiring it via a gitignored `.envrc`).

```sh
agent-mesh task add --repo <repo> --prompt "fix the flaky test" [--host <host-name>] [--budget 5]
agent-mesh task list
agent-mesh report --since 16h
```

## Claude Code integration (opt-in)

`install.sh` never touches `~/.claude*`. To let interactive sessions use the hub, run on each host:

```sh
agent-mesh setup-claude --dry-run   # review the diff
agent-mesh setup-claude             # user-scope MCP "agentmesh" + additive hooks
agent-mesh setup-claude --uninstall # removes only what it added
```

Setup adds hook entries (`SessionStart`, `UserPromptSubmit`, `Stop`, `SessionEnd`) to `~/.claude/settings.json` (backup: `settings.json.agentmesh-bak`) and one user-scope MCP server. It never changes existing skills, rules, or hooks. Restart running sessions afterwards. Hooks fail open: if the hub is down they exit 0 silently.

Message delivery: at the next prompt, at turn end (one Stop block), and to an idle session through a background `agent-mesh inbox --wait` waiter that the session starts itself. If waking an idle session does not work in your Claude Code version, idle sessions receive messages at their next prompt only.

## Tokens

| Kind | Created with | Can do |
|---|---|---|
| admin | `token new --admin` | everything, including cancel and requeue |
| host | `token new --host <name>` | daemon: lease ops, load reports, reads |
| session | `token new --session <name>` | interactive sessions: sessions, messages, task add, reads |

Tokens are 32 random bytes in 0600 files outside the repo. The hub stores only SHA-256 hashes and derives the host from the token.

## Security notes

- Interactive sessions may run with `bypassPermissions`. Any message from the hub is untrusted data: hooks frame it as such, and you must not treat message text as instructions with authority.
- A session token lets its holder message every session and add tasks. Treat all your hosts as one trust domain.
- Workers cannot push, run `gh` or `curl`, load the agentmesh MCP, or read user hooks. The daemon pushes only after a gitleaks scan and a fence check. Tasks name a tool profile; they cannot grant tools.
- The hub listens on loopback by default. Never bind to a LAN or public address.
- This repo is public: keep tokens, IPs, hostnames, and usernames out. `make lint-secrets` runs gitleaks.

## Limits

Ubuntu hub only in v1 (no hub when that machine is off; daemons back off and leases get grace on return). Manual backup only. Mac daemon is optional and needs Tailscale. Idle wake is best effort.

## Troubleshooting

- `agent-mesh hosts` shows `unhealthy:<err>`: run `journalctl --user -u agent-mesh-daemon -n 50`; usual cause is `claude`, `gh`, or `gitleaks` missing from the unit's PATH or an expired credential.
- Services dead after reboot: check `loginctl show-user "$USER" | grep Linger` and the network autoconnect setting.
- Hub bind fails: another process holds the port, or the Tailscale address is not up yet (the hub retries for 2 minutes).
- Hook silent: expected when the hub is unreachable; check `agent-mesh hosts`.

## Development

`make build test vet cross lint-secrets clean`. Binaries land in `bin/` and `dist/`.
