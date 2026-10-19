# Usage guide

Everything the [README](../README.md) leaves out: install and update details, what each setup command does, how a task travels, machine roles, the Claude Code integration and the terminal commands.

Related: [security.md](security.md) (tokens, trust model), [troubleshooting.md](troubleshooting.md) (fixes and limits), [deployment-guide.md](deployment-guide.md) (manual setup, daemon config), [system-architecture.md](system-architecture.md) (design).

## Install

Linux or macOS (amd64/arm64), no sudo:

```sh
curl -fsSL https://raw.githubusercontent.com/felixn678/agent-mesh/main/scripts/install.sh | sh
```

The script downloads the latest release archive for your OS and CPU, checks it against the release's `SHA256SUMS`, and installs `agent-mesh` to `~/.local/bin` (it tells you if that is not on your PATH). Pin a version with `AGENTMESH_VERSION=v0.1.0`, pick another directory with `AGENTMESH_INSTALL_DIR`. Read the script first if you like: [scripts/install.sh](../scripts/install.sh).

With Go installed instead:

```sh
go install github.com/felixn678/agent-mesh/cmd/agent-mesh@latest
```

Check with `agent-mesh version`, then follow the README's Quick start: `agent-mesh up` on the hub machine, or `agent-mesh join ...` (from `agent-mesh invite` on the hub) on the others, then `agent-mesh setup-claude` and `agent-mesh doctor`.

## Updating

```sh
agent-mesh update --check   # exit 0: up to date, 10: a newer release exists
agent-mesh update           # install the latest release, restart this machine's services
```

`update` downloads the same archive and `SHA256SUMS` as the installer, refuses a checksum mismatch, replaces the running binary in place and restarts the hub and daemon services that `up`/`join` installed (hub first; `--no-restart` skips this). Claude hooks and the MCP server pick up the new binary on their next run. Update the hub machine first, then the others. To roll back, name a release: `agent-mesh update --version v0.1.0`. A binary from `go install` or `go run` is not replaced: `update` prints the `go install ...@<tag>` command instead.

## Setup commands

What the commands do:

- `up` is idempotent: re-running keeps existing tokens and configs. On its first run with Tailscale connected it listens on the tailnet address (`<tailscale-ip>:47878`), otherwise on `127.0.0.1:47878`; `--listen <addr>` picks another one and wildcard addresses are refused. The default port stays clear of common dev servers (3000, 5173, 8080, Expo's 8081, wrangler's 8787). Other flags: `--name <host>` (default: short hostname), `--no-service` (write config only; then run `agent-mesh hub` yourself).
- `invite <name>` names the new machine; the name is only a label (any OS). It reloads the hub (SIGHUP, no restart). The code holds the new machine's tokens and the hub address: send it over a private channel only. Lost it? The hub keeps only token hashes, so run `agent-mesh invite <name> --reissue` for a new one (the old code and that machine's current tokens stop working, so it runs `join` again). If the hub listens on loopback, `invite` warns that other machines cannot reach it.
- `join` verifies the tokens against the hub before writing anything. Entering the code at the prompt keeps it out of shell history; `agent-mesh join <code>` also works. `agent-mesh join <hub-url> <code>` overrides the address in the code.
- `up` and `join` finish by running `agent-mesh setup-claude` (see [Claude Code integration](#claude-code-integration)), which adds only agent-mesh's own hooks and MCP server; `--no-claude` skips it.

### The daemon (optional)

Open Claude sessions take the tasks queued to their machine. To also work on tasks while no session is open, add the daemon, which runs headless `claude -p` workers: `agent-mesh up --daemon`, `agent-mesh join --daemon`, or later `agent-mesh daemon install`. It also needs `gh` (logged in) and `gitleaks`; `doctor` checks them only where the daemon is installed. Before it takes tasks, add the repos it may work on to `[repos]` in its `daemon.toml` (see [Daemon config](deployment-guide.md#daemon-config)) and give it GitHub credentials in `gh.env` (or a working `gh auth login`). Machines set up before the daemon became optional keep theirs.

Building from a checkout instead (Go 1.26): `make build && install -m 0755 bin/agent-mesh ~/.local/bin/agent-mesh`. Keep the binary at a stable path: services and Claude hooks run it by its absolute path.

Manual setup (tokens, units, editing `hub.toml` by hand) is still supported: see [deployment-guide.md](deployment-guide.md).

## How a task travels

Once every machine has run `setup-claude`, run `/am:on` in the session that should coordinate on each machine (typically your lead session). The lead splits work, hands local parts to the other sessions on its machine as usual, and talks to the lead on the other machine through the mesh. For example, in a lead session inside `~/Code/<repo>` on your Linux box:

> Split this refactor into 3 tasks. Run 2 of them on `<mac-host>`.

1. Claude calls the `task_add` MCP tool three times, passing its own session id as `from`. The repo defaults to this session's git remote (e.g. `github.com/<user>/<repo>`). Two tasks get `target_host = <mac-host>`; one has no target.
2. On `<mac-host>`, an idle Claude session open in the same repo has a background waiter (`agent-mesh inbox --wait`, started by the session itself). The waiter exits as soon as claimable work exists, which wakes that session.
3. The woken session calls `task_claim`, works on a new branch, pushes, and calls `task_done`. Its lease is extended at each turn end while it works.
4. The hub messages your original session. You see the result at your next prompt, when the current turn ends, or immediately if your session is idle with its waiter running.
5. The untargeted task goes to whichever eligible taker asks first: an idle session of that repo on any machine, or a daemon whose `[repos]` allowlist contains the repo.

## Machine roles

Tell agent-mesh once what each machine is for. Sessions then route work without you naming a machine:

```sh
agent-mesh host-note "backend, API, DB, web frontend; mobile apps only via the web build"        # on <linux-host>
agent-mesh host-note --host <mac-host> "frontend, React Native/Expo; Xcode, iOS simulator, adb"  # hub machine (admin token)
agent-mesh host-note                                                                             # show every machine
```

A session joining the mesh also reports the machine's OS and the tools it finds (Xcode, adb, the Android emulator, Chrome, node, go, docker, …). When the mesh has two or more machines, every session in the mesh gets a short briefing: each machine's note, tools and live session count, plus the rule. A machine you name always wins. Otherwise the session queues work that clearly fits another machine better (for example "build the Expo login screen and test it on the iOS simulator") with `target_host` for that machine, tells you which machine it chose and why, and does fitting work itself. A queued task runs only once a session in the mesh (or a daemon) on that machine has the same repo open; the session checks `sessions` and says so when none is open. A session token can only set its own machine's note; the admin token on the hub machine can set any. Notes are at most 300 bytes and are shown to sessions as your own description, quoted.

Useful phrases inside a session: "list agentmesh sessions", "send `<name or id prefix>`: …", "show my inbox", "what is task 12 doing?". From a terminal: `agent-mesh task list`, `agent-mesh sessions`, `agent-mesh hosts`, `agent-mesh report`.

Messages from other sessions are shown as untrusted data, never as instructions.

## Claude Code integration

`up` and `join` run `agent-mesh setup-claude` at the end (skip with `--no-claude`); `deploy/install.sh` and `doctor` never touch `~/.claude*`. Sessions are out of the mesh by default. In a session:

- `/am:on` puts it in: it registers with the hub, gets its briefing, starts its message waiter, and can be messaged and woken by other machines. The choice is kept for that session across `/compact` and resume.
- `/am:off` takes it out again: the hub marks it ended and any task it holds is handed back.
- `AGENTMESH=on claude` starts a session already in the mesh.

Run `setup-claude` yourself to preview, repair or remove the integration:

```sh
agent-mesh setup-claude --dry-run   # print the settings diff and the claude command; change nothing
agent-mesh setup-claude             # user-scope MCP "agentmesh" + additive hooks + /am:on, /am:off
agent-mesh setup-claude --force     # also replace a user-scope "agentmesh" server it did not write
agent-mesh setup-claude --uninstall # removes only what it added
```

Setup needs `claude` on PATH and `session.token`. It appends one hook entry each to `SessionStart`, `UserPromptSubmit`, `Stop` and `SessionEnd` in `~/.claude/settings.json` (or `$CLAUDE_CONFIG_DIR/settings.json`) by splicing bytes, so existing keys, hooks and formatting stay as they are, and writes the `/am:on` and `/am:off` commands to their own folder `~/.claude/commands/am/` (a file there without its `managed-by=agent-mesh` marker is left alone); backup `settings.json.agentmesh-bak`, atomic write, symlinks followed. Its entries end in `--managed-by=agent-mesh`; only those are ever changed or removed. Re-running repairs them (and the MCP entry) after the binary moved or the hub URL changed; it refuses to run from a `go run` build. It registers the MCP server with `claude mcp add-json -s user` using a `headersHelper` (`agent-mesh hook mcp-headers`) that reads `session.token`, so no token is written to `~/.claude.json` or your shell env. It never changes existing skills, rules, or hooks. Restart running sessions afterwards. When a session ends, its `SessionEnd` hook marks it ended and releases any task it still holds (no attempt is spent), so another session can take it right away. Hooks of a session outside the mesh exit at once without reading a token or calling the hub. Hooks fail open: if the hub is down or a token is missing they exit 0 silently within 800 ms and log to the hook log. `AGENTMESH_HOOKS=off` disables them (set for daemon workers). Name a session with `/rename <name>` in Claude (picked up on the next prompt) or `AGENTMESH_SESSION_NAME=<name>` before starting `claude` (wins over `/rename`); `send` matches names case-insensitively; start it with `AGENTMESH_WORKER=off` to opt it out of claiming tasks (its waiter then wakes only for messages and `task_claim` is refused).

MCP tools: `task_add` (`target_host`, `target_session`, `from`: repo defaults to the caller session's git remote; `request_id` makes retries idempotent, and `task add` on the CLI sets one automatically), `task_list`, `task_get`, `hosts` (machines with their notes, tools and live sessions, plus daemon load), `morning_report`, `sessions`, `send` (to an id, a 4+ character prefix, or a name), and with a session token `inbox`, `task_claim`, `task_heartbeat`, `task_done`, `task_fail`, `task_release`; admin only: `task_cancel`, `task_requeue`.

Version skew: the hub sends its version on every response (`X-AgentMesh-Version`, and `hub_version` in `/api/v1/whoami`), and sessions and daemons report theirs. When a machine runs a different release than the hub, `agent-mesh hosts`/`sessions` and the MCP `hosts`/`sessions` tools mark its rows (``version v0.1.0 ≠ hub v0.1.2 → run `agent-mesh update` there``), `doctor` warns, and a new session gets one line saying where to run `agent-mesh update`. Dev builds, pseudo-versions and older hubs or peers that report no version are not compared.

Message delivery is at least once: messages are acked only after they were shown. They arrive at the next prompt, at turn end (one Stop block), or through a background `agent-mesh inbox --wait --session <id>` waiter that the session starts itself (`SessionStart` tells it how). The waiter exits, waking the idle session, when a message arrives or when this session can claim new work for its repo. It only announces messages: Claude Code runs the `UserPromptSubmit` hook for the wake-up turn, and that hook injects and acks them (output of a background command may never be read). The session then claims with `task_claim`, works on a new branch, pushes, and reports with `task_done`; the hub messages the requester. The Stop hook extends the lease of a task the session holds. If waking an idle session does not work in your Claude Code version, idle sessions receive messages and work at their next prompt only.

## Terminal commands

```sh
agent-mesh task add --repo <repo> --prompt "fix the flaky test" --budget 2 [--host <host-name>]
agent-mesh task add --repo <repo> --budget 2 --prompt-file - < spec.md   # prompt from stdin
agent-mesh task list [--state queued] [--since 16h]
agent-mesh task get <id>
agent-mesh task cancel|requeue <id>      # hub machine only (admin token)
agent-mesh hosts [--verbose] [--sessions]   # daemon load per machine; --sessions also lists sessions
agent-mesh sessions                        # live Claude sessions on every machine (worker:off = opted out)
agent-mesh host-note [--host <name>] <text>|--clear   # what a machine is for; no args lists every machine
agent-mesh report [--since 16h] [--out <file>]
agent-mesh task -h                       # each subcommand also takes -h
```

`--budget` (USD, > 0, at most 100) is required. `--repo` is either a git remote (`https://github.com/<user>/<repo>.git`, `git@github.com:<user>/<repo>`; stored as `github.com/<user>/<repo>`) or a short key; it must match a daemon's `[repos]` key or a session's repo to be picked up. Other `task add` flags: `--base` (default `main`), `--mode acceptEdits|dontAsk|plan` (default `acceptEdits`), `--profile` (host tool profile, default `default`), `--max-attempts` (default 3).

## Where things live

| What | Linux | macOS |
|---|---|---|
| config dir: tokens, `hub.toml`, `daemon.toml`, `client.toml`, `gh.env` | `~/.config/agent-mesh` (`$XDG_CONFIG_HOME`) | `~/Library/Application Support/agent-mesh` |
| data: `hub.db`, daemon `repos/ worktrees/ logs/ spool/ run/` | `~/.local/share/agent-mesh` | `~/.local/share/agent-mesh` |
| hook log | `~/.local/state/agent-mesh/hook.log` (`$XDG_STATE_HOME`) | same |
| service logs | `journalctl --user -u agent-mesh-hub` / `-daemon` | `~/Library/Logs/agent-mesh/` |

`AGENTMESH_CONFIG_DIR=/abs/path` overrides the config dir on both systems (for example a second identity on one machine). Set it before `up`/`join` (the services they install inherit it) and export it in your shell profile so Claude sessions, whose hooks and MCP helper run `agent-mesh`, use the same directory. It must be an absolute path.

CLI commands resolve the hub URL as `$AGENTMESH_URL`, else `hub_url` in `client.toml`, else in `daemon.toml`, else `http://127.0.0.1:47878`. Tokens: `$AGENTMESH_TOKEN`, else `session.token` (task add/list/get, hosts); cancel, requeue and `report` prefer `admin.token`. Hooks, the waiter and the MCP server use only the session token (`$AGENTMESH_SESSION_TOKEN` or `session.token`) and send it only to the hub in `client.toml`/`daemon.toml`: they ignore `$AGENTMESH_URL`, which a project's `.claude/settings.json` could set.
