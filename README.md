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
agent-mesh join <hub-url>          # on the new machine; paste the printed code when asked (keeps it out of shell history)
agent-mesh setup-claude && agent-mesh doctor
```

The invite line contains the new machine's tokens: send it over a private channel only. `join` verifies the tokens against the hub before writing anything.

Manual setup (tokens, units, editing `hub.toml` by hand) is still supported: see [docs/deployment-guide.md](docs/deployment-guide.md).

CLI commands resolve the hub URL as `$AGENTMESH_URL`, else `hub_url` in `client.toml`, else in `daemon.toml` (config dir `~/.config/agent-mesh`), else `http://127.0.0.1:8787`. Tokens: `$AGENTMESH_TOKEN`, else `session.token` (task add/list/get, hosts); cancel, requeue and `report` prefer `admin.token`. Hooks, the waiter and the MCP server use only the session token (`$AGENTMESH_SESSION_TOKEN` or `session.token`).

```sh
agent-mesh task add --repo <repo> --prompt "fix the flaky test" [--host <host-name>] [--budget 5]
agent-mesh task add --repo <repo> --prompt-file - < spec.md    # prompt from stdin
agent-mesh task list [--state queued] [--since 16h]
agent-mesh task get|cancel|requeue <id>
agent-mesh hosts [--verbose]
agent-mesh report --since 16h
```

## Claude Code integration (opt-in)

`install.sh` never touches `~/.claude*`. To let interactive sessions use the hub, run on each host:

```sh
agent-mesh setup-claude --dry-run   # print the settings diff and the claude command; change nothing
agent-mesh setup-claude             # user-scope MCP "agentmesh" + additive hooks
agent-mesh setup-claude --force     # replace a different existing user-scope "agentmesh" server
agent-mesh setup-claude --uninstall # removes only what it added
```

Setup needs `claude` on PATH and `session.token`. It appends one hook entry each to `SessionStart`, `UserPromptSubmit`, `Stop` and `SessionEnd` in `~/.claude/settings.json` (or `$CLAUDE_CONFIG_DIR/settings.json`) by splicing bytes, so existing keys, hooks and formatting stay as they are; backup `settings.json.agentmesh-bak`, atomic write, symlinks followed. It registers the MCP server with `claude mcp add-json -s user` using a `headersHelper` (`agent-mesh hook mcp-headers`) that reads `session.token`, so no token is written to `~/.claude.json` or your shell env. It never changes existing skills, rules, or hooks. Restart running sessions afterwards. Hooks fail open: if the hub is down or a token is missing they exit 0 silently within 800 ms and log to `~/.local/state/agent-mesh/hook.log`. `AGENTMESH_HOOKS=off` disables them (set for daemon workers). Name a session with `AGENTMESH_SESSION_NAME=<name>`.

MCP tools: `task_add` (`target_host`, `target_session`, `from`: repo defaults to the caller session's git remote), `task_list`, `task_get`, `hosts`, `morning_report`, `sessions`, `send` (to an id, a 4+ character prefix, or a name), and with a session token `inbox`, `task_claim`, `task_heartbeat`, `task_done`, `task_fail`, `task_release`; admin only: `task_cancel`, `task_requeue`. In this repo `.mcp.json` shadows the user-scope server and reads `$AGENTMESH_SESSION_TOKEN`.

Message delivery is at least once: messages are acked only after they were shown. They arrive at the next prompt, at turn end (one Stop block), or through a background `agent-mesh inbox --wait --session <id>` waiter that the session starts itself (`SessionStart` tells it how). The waiter exits, waking the idle session, when a message arrives or when this session can claim new work for its repo. The session then claims with `task_claim`, works on a new branch, pushes, and reports with `task_done`; the hub messages the requester. The Stop hook extends the lease of a task the session holds. If waking an idle session does not work in your Claude Code version, idle sessions receive messages and work at their next prompt only.

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
