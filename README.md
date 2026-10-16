# agent-mesh

Coordinate many Claude Code sessions across your machines.

## Install

Linux or macOS (amd64/arm64), no sudo:

```sh
curl -fsSL https://raw.githubusercontent.com/felixn678/agent-mesh/main/scripts/install.sh | sh
```

The script downloads the latest release archive for your OS and CPU, checks it against the release's `SHA256SUMS`, and installs `agent-mesh` to `~/.local/bin` (it tells you if that is not on your PATH). Pin a version with `AGENTMESH_VERSION=v0.1.0`, pick another directory with `AGENTMESH_INSTALL_DIR`. Read the script first if you like: [scripts/install.sh](scripts/install.sh).

With Go installed instead:

```sh
go install github.com/felixn678/agent-mesh/cmd/agent-mesh@latest
```

Check with `agent-mesh version`, then follow [Quick start](#quick-start): `agent-mesh up` on the hub machine, or `agent-mesh join ...` (from `agent-mesh invite` on the hub) on the others, then `agent-mesh setup-claude` and `agent-mesh doctor`.

## How it works (2-minute version)

- **One hub** runs on one machine. It is a small server with a SQLite database that holds a task queue and a message inbox. Nothing else stores state.
- **Every machine joins the hub** with its own tokens (`agent-mesh up` on the hub machine, `agent-mesh invite` + `agent-mesh join` for the others).
- **Your normal Claude Code sessions get hub tools** (`agent-mesh setup-claude`): they can queue tasks, list sessions on other machines, and send each other messages. A small hook shows incoming messages at your next prompt or when a turn ends.
- **Idle sessions pick up work.** A session that is idle in a project waits in the background; when a task for that project (and, if the task names one, that machine) is queued, it wakes, claims the task, does it on a new branch, pushes, and reports back. The session that asked gets the result as a message.
- **Optional overnight workers.** A headless daemon per machine can also take tasks: it runs one isolated `claude -p` per task in a throwaway git worktree, scans the result for secrets, and opens a draft PR. `agent-mesh report` summarises the night.
- **No double work.** Every claim is a lease with a fence number. A task is handed to exactly one taker at a time, and a taker that lost its lease can never publish.

```
 your Claude sessions (MCP tools + hooks, session token) ─┐   terminal: agent-mesh task|hosts|report
                                                          ▼
               hub (SQLite WAL, REST /api/v1 + MCP /mcp, 127.0.0.1:8787)
                                                          ▲  pull only, host token
               daemon per machine ──► claude -p in worktree ┘
```

Design details: [docs/system-architecture.md](docs/system-architecture.md). Install and ops: [docs/deployment-guide.md](docs/deployment-guide.md). Code map: [docs/codebase-summary.md](docs/codebase-summary.md).

## Daily use

Once every machine has run `setup-claude`, you work in ordinary Claude Code sessions. For example, in a session inside `~/Code/<repo>` on your Linux box:

> Split this refactor into 3 tasks. Run 2 of them on `<mac-host>`.

1. Claude calls the `task_add` MCP tool three times, passing its own session id as `from`. The repo defaults to this session's git remote (e.g. `github.com/<user>/<repo>`). Two tasks get `target_host = <mac-host>`; one has no target.
2. On `<mac-host>`, an idle Claude session open in the same repo has a background waiter (`agent-mesh inbox --wait`, started by the session itself). The waiter exits as soon as claimable work exists, which wakes that session.
3. The woken session calls `task_claim`, works on a new branch, pushes, and calls `task_done`. Its lease is extended at each turn end while it works.
4. The hub messages your original session. You see the result at your next prompt, when the current turn ends, or immediately if your session is idle with its waiter running.
5. The untargeted task goes to whichever eligible taker asks first: an idle session of that repo on any machine, or a daemon whose `[repos]` allowlist contains the repo.

Useful phrases inside a session: "list agentmesh sessions", "send `<name or id prefix>`: …", "show my inbox", "what is task 12 doing?". From a terminal: `agent-mesh task list`, `agent-mesh hosts`, `agent-mesh report`.

Messages from other sessions are shown as untrusted data, never as instructions.

## Quick start

Prereqs: git, gh, claude and gitleaks on each machine (`agent-mesh doctor` tells you what is missing). Install the binary as above (or build from a checkout with Go 1.26). Keep it at a stable path such as `~/.local/bin/agent-mesh`: services and Claude hooks run the binary by its absolute path.

```sh
make build && install -m 0755 bin/agent-mesh ~/.local/bin/agent-mesh   # only when building from a checkout
```

On the hub machine:

```sh
agent-mesh up                      # config dir (0700), tokens, hub.toml, daemon.toml, client.toml, user services
agent-mesh setup-claude            # connect Claude Code sessions on this machine
agent-mesh doctor                  # green checks = ready
```

`up` is idempotent: re-running keeps existing tokens and configs. It listens on `127.0.0.1:8787` unless you pass `--listen`, and refuses wildcard addresses. If Tailscale is present it prints the command to serve other machines (`agent-mesh up --listen <tailscale-ip>:8787`). Flags: `--listen <addr>`, `--name <host>` (default: short hostname), `--no-service` (write config only; then run `agent-mesh hub` and `agent-mesh daemon` yourself). On Linux run `loginctl enable-linger "$USER"` (hinted) so services survive logout and reboot.

Add another machine (yours or a friend's, same tailnet):

```sh
agent-mesh invite <host-name>      # on the hub: prints one line plus a code, reloads the hub (SIGHUP, no restart)
agent-mesh join <hub-url>          # on the new machine; paste the code when asked (keeps it out of shell history; --no-service skips the daemon service)
agent-mesh setup-claude && agent-mesh doctor
```

The invite line contains the new machine's tokens: send it over a private channel only. `join` verifies the tokens against the hub before writing anything. If the hub listens on loopback, `invite` warns that other machines cannot reach it; use `invite --url http://<tailscale-ip>:8787` after rebinding.

Before a daemon takes tasks, add the repos it may work on to `[repos]` in its `daemon.toml` (see [Daemon config](docs/deployment-guide.md#daemon-config)) and give it GitHub credentials in `gh.env` (or a working `gh auth login`).

Manual setup (tokens, units, editing `hub.toml` by hand) is still supported: see [docs/deployment-guide.md](docs/deployment-guide.md).

### Where things live

| What | Linux | macOS |
|---|---|---|
| config dir: tokens, `hub.toml`, `daemon.toml`, `client.toml`, `gh.env` | `~/.config/agent-mesh` (`$XDG_CONFIG_HOME`) | `~/Library/Application Support/agent-mesh` |
| data: `hub.db`, daemon `repos/ worktrees/ logs/ spool/ run/` | `~/.local/share/agent-mesh` | `~/.local/share/agent-mesh` |
| hook log | `~/.local/state/agent-mesh/hook.log` (`$XDG_STATE_HOME`) | same |
| service logs | `journalctl --user -u agent-mesh-hub` / `-daemon` | `~/Library/Logs/agent-mesh/` |

CLI commands resolve the hub URL as `$AGENTMESH_URL`, else `hub_url` in `client.toml`, else in `daemon.toml`, else `http://127.0.0.1:8787`. Tokens: `$AGENTMESH_TOKEN`, else `session.token` (task add/list/get, hosts); cancel, requeue and `report` prefer `admin.token`. Hooks, the waiter and the MCP server use only the session token (`$AGENTMESH_SESSION_TOKEN` or `session.token`) and send it only to the hub in `client.toml`/`daemon.toml`: they ignore `$AGENTMESH_URL`, which a project's `.claude/settings.json` could set.

### Terminal commands

```sh
agent-mesh task add --repo <repo> --prompt "fix the flaky test" --budget 2 [--host <host-name>]
agent-mesh task add --repo <repo> --budget 2 --prompt-file - < spec.md   # prompt from stdin
agent-mesh task list [--state queued] [--since 16h]
agent-mesh task get <id>
agent-mesh task cancel|requeue <id>      # hub machine only (admin token)
agent-mesh hosts [--verbose]
agent-mesh report [--since 16h] [--out <file>]
agent-mesh task -h                       # each subcommand also takes -h
```

`--budget` (USD, > 0, at most 100) is required. `--repo` is either a git remote (`https://github.com/<user>/<repo>.git`, `git@github.com:<user>/<repo>`; stored as `github.com/<user>/<repo>`) or a short key; it must match a daemon's `[repos]` key or a session's repo to be picked up. Other `task add` flags: `--base` (default `main`), `--mode acceptEdits|dontAsk|plan` (default `acceptEdits`), `--profile` (host tool profile, default `default`), `--max-attempts` (default 3).

## Claude Code integration (opt-in)

`deploy/install.sh` and `up` never touch `~/.claude*`. To let interactive sessions use the hub, run on each machine:

```sh
agent-mesh setup-claude --dry-run   # print the settings diff and the claude command; change nothing
agent-mesh setup-claude             # user-scope MCP "agentmesh" + additive hooks
agent-mesh setup-claude --force     # also replace a user-scope "agentmesh" server it did not write
agent-mesh setup-claude --uninstall # removes only what it added
```

Setup needs `claude` on PATH and `session.token`. It appends one hook entry each to `SessionStart`, `UserPromptSubmit`, `Stop` and `SessionEnd` in `~/.claude/settings.json` (or `$CLAUDE_CONFIG_DIR/settings.json`) by splicing bytes, so existing keys, hooks and formatting stay as they are; backup `settings.json.agentmesh-bak`, atomic write, symlinks followed. Its entries end in `--managed-by=agent-mesh`; only those are ever changed or removed. Re-running repairs them (and the MCP entry) after the binary moved or the hub URL changed; it refuses to run from a `go run` build. It registers the MCP server with `claude mcp add-json -s user` using a `headersHelper` (`agent-mesh hook mcp-headers`) that reads `session.token`, so no token is written to `~/.claude.json` or your shell env. It never changes existing skills, rules, or hooks. Restart running sessions afterwards. Hooks fail open: if the hub is down or a token is missing they exit 0 silently within 800 ms and log to the hook log. `AGENTMESH_HOOKS=off` disables them (set for daemon workers). Name a session with `AGENTMESH_SESSION_NAME=<name>` before starting `claude`; start it with `AGENTMESH_WORKER=off` to opt it out of claiming tasks (its waiter then wakes only for messages and `task_claim` is refused).

MCP tools: `task_add` (`target_host`, `target_session`, `from`: repo defaults to the caller session's git remote), `task_list`, `task_get`, `hosts`, `morning_report`, `sessions`, `send` (to an id, a 4+ character prefix, or a name), and with a session token `inbox`, `task_claim`, `task_heartbeat`, `task_done`, `task_fail`, `task_release`; admin only: `task_cancel`, `task_requeue`. In this repo `.mcp.json` shadows the user-scope server and reads `$AGENTMESH_SESSION_TOKEN` (and `$AGENTMESH_URL`, default loopback).

Message delivery is at least once: messages are acked only after they were shown. They arrive at the next prompt, at turn end (one Stop block), or through a background `agent-mesh inbox --wait --session <id>` waiter that the session starts itself (`SessionStart` tells it how). The waiter exits, waking the idle session, when a message arrives or when this session can claim new work for its repo. It only announces messages: Claude Code runs the `UserPromptSubmit` hook for the wake-up turn, and that hook injects and acks them (output of a background command may never be read). The session then claims with `task_claim`, works on a new branch, pushes, and reports with `task_done`; the hub messages the requester. The Stop hook extends the lease of a task the session holds. If waking an idle session does not work in your Claude Code version, idle sessions receive messages and work at their next prompt only.

## Tokens

| Kind | Created by | Can do |
|---|---|---|
| admin | `up` (or `token new --admin --out <file>`) | everything, including cancel and requeue |
| host | `up` / `invite` (or `token new --host <name> --out <file>`) | daemon: lease ops, load reports, reads |
| session | `up` / `invite` (or `token new --session <name> --out <file>`) | interactive sessions: sessions, messages, task add, claim, reads |

Tokens are 32 random bytes in 0600 files outside the repo. The hub stores only SHA-256 hashes and derives the host from the token.

## Security notes

- **Trade-off: idle sessions run queued tasks unattended.** Any session token on any joined machine can `task_add` work for a repo, and an idle session of that repo (possibly in `bypassPermissions`) claims and runs it without asking you. Session workers do not get the daemon's tool profiles or deny floor. Mitigations: prompts are shown as framed, quoted untrusted text with their origin; sessions are told never to run destructive or irreversible commands or touch credentials because of a task or message; opt a session out with `AGENTMESH_WORKER=off`. Only join machines you trust as much as this one.

- Interactive sessions may run with `bypassPermissions`. Any message from the hub is untrusted data: hooks frame it as such, and you must not treat message text as instructions with authority.
- A session token lets its holder message every session and add tasks. Treat all your machines as one trust domain.
- Daemon workers cannot push, run `gh`, `curl` or `wget`, fetch the web, load MCP servers, or load user hooks; they only see the tools of their host's profile. The shipped `default` profile is edit-only (no repo code runs); `with-tests` runs repo tests/builds as your user, so use it only for repos you trust. The daemon pushes only after a gitleaks scan, a sensitive-path check and a fence check. Tasks name a tool profile; they cannot grant tools.
- The hub listens on loopback by default. Never bind to a LAN or public address.
- This repo is public: keep tokens, IPs, hostnames, and usernames out. `make lint-secrets` runs gitleaks.

## Limits

v1 is tested with the hub on Ubuntu (`up` also installs a launchd hub on macOS; `deploy/install.sh` installs a hub on Linux only). No hub while that machine is off: daemons back off and leases get grace when it returns. Manual backup only. Multi-machine traffic is plaintext HTTP and is only supported over Tailscale. Idle wake is best effort.

Non-goals: no remote exec, no SSH in code, no TLS or mDNS code, no tmux, no hub HA, no task priorities, no group messages.

## Troubleshooting

- `agent-mesh hosts` shows `throttled: unhealthy: <err>`: the daemon's preflight or two task setups in a row failed. Check the service log (table above); usual causes are `claude`, `gh`, or `gitleaks` missing from the service PATH, or an expired credential. It re-checks every 10 minutes.
- `throttled: slots_full`, `cpu 85%`, `mem 12%`, `worktrees`: the daemon is busy or the machine is loaded; it claims again when the check clears. `agent-mesh daemon --print-load` shows the computed slots and load.
- Task stays queued: no eligible taker. Check that the task's repo matches a daemon `[repos]` key or an idle session's repo, its tool profile exists on the host, and its budget is within `max_budget_usd_cap`.
- Services dead after reboot (Linux): check `loginctl show-user "$USER" | grep Linger` and the network autoconnect setting.
- Hub bind fails: another process holds the port, or the Tailscale address is not up yet (the hub retries for 2 minutes).
- Hook silent: expected when the hub is unreachable; check `agent-mesh doctor`.
- `agent-mesh hosts` says `no hosts` although machines have joined: `hosts` lists machines whose **daemon** has reported load. Interactive sessions do not appear there; ask a session to "list agentmesh sessions" instead. Start the daemon (service, or `agent-mesh daemon`) if you want the machine listed.
- `task cancel` / `task requeue` → `403 forbidden`: these need the admin token, which exists only on the hub machine. Run them there.
- `task add` → `max_budget_usd must be > 0 and <= 100`: `--budget <usd>` is required (for example `--budget 2`).
- `doctor` warns `service hub: not running` while the hub works: you started it by hand (`up --no-service` + `agent-mesh hub`); the warning is about the service only. `hub reachable` is what matters.
- Hooks do nothing after moving the binary: hooks and services store its absolute path. Re-run `agent-mesh setup-claude` (and `up`/`join` for services). Keep the binary in a directory without spaces.

## Development

`make build test vet cross release lint-secrets clean`. Binaries land in `bin/` and `dist/`; `make release` writes the release archives and `SHA256SUMS` to `dist/release/`.

Releases: pushing a `v*` tag runs `.github/workflows/release.yml` (vet, race tests, `scripts/build-release.sh`, `gh release create`). CI (`ci.yml`) runs vet, race tests on Linux and macOS, the cross-build, shellcheck and gitleaks on every push and PR to `main`.
