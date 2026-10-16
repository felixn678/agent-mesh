# Deployment guide

All names in angle brackets are placeholders. Never commit real values.

`<config-dir>` below is `~/.config/agent-mesh` on Linux (`$XDG_CONFIG_HOME/agent-mesh` if set) and `~/Library/Application Support/agent-mesh` on macOS. Data lives in `~/.local/share/agent-mesh` on both.

## Prerequisites (per host)

git, gh, claude (logged in), gitleaks (`go install github.com/zricethezav/gitleaks/v8@<pinned>` or a release binary). Binary: the one-line installer or `go install` (see README "Install"), or build with Go 1.26: `make build` writes `bin/agent-mesh`; `make cross` writes `dist/agent-mesh-{linux-amd64,linux-arm64,darwin-arm64,darwin-amd64}`. Install the binary to a stable path (for example `~/.local/bin/agent-mesh`) before `up`, `join` or `setup-claude`: services and Claude hooks call it by its resolved absolute path.

## Fast path: `up`, `invite`, `join`, `doctor`

| Command | Where | What it does |
|---|---|---|
| `agent-mesh up [--listen addr] [--name host] [--no-service]` | hub machine | creates `<config-dir>` (0700), `admin.token`, `host.token`, `session.token` (0600), `hub.toml`, `daemon.toml`, `client.toml`; installs and starts the hub and daemon user services (systemd `--user` on Linux, launchd on macOS). Idempotent. `--listen` on an existing setup rewrites `listen` and `hub_url` and restarts the hub. |
| `agent-mesh invite <host-name> [--url u]` | hub machine | mints host and session tokens, appends their hashes to `hub.toml` (comments preserved, file validated before it replaces the old one), signals the hub to reload, prints `agent-mesh join <hub-url> <bundle>`. Warns when the URL is loopback. |
| `agent-mesh join <hub-url> <bundle> [--no-service]` | other machine | verifies both tokens against the hub, then writes `host.token`, `session.token`, `daemon.toml`, `client.toml` and installs the daemon service. |
| `agent-mesh doctor` | any | checks config dir, token modes, hub reachability, each token's auth, `claude auth status`, git, `gh auth status`, gitleaks, services, Tailscale. Exit status is non-zero if a required check fails (services and Tailscale are warnings only). |

The bundle is base64url of `{"v":1,"url","host","host_token","session_token"}`; treat it as a secret. The hub reloads its token tables (admin, host, session) on `SIGHUP`; a `hub.toml` that fails validation is rejected and the old tokens stay active. Listen address, database and timings still need a restart. While running, the hub writes `hub.pid` next to `hub.toml` so `invite` can signal it when no service manager owns the hub. `client.toml` holds `hub_url` for CLI commands (`AGENTMESH_URL` overrides it).

`up` binds loopback unless you pass `--listen`, and refuses wildcard addresses. Unit files are rewritten only when their content changes, and only then is the service restarted. With `--no-service`, run `agent-mesh hub` and `agent-mesh daemon` yourself.

After `up` or `join`, a daemon has no repos yet: edit `[repos]` in `daemon.toml` and provide `gh.env` (below), then restart the daemon service.

## Daemon config

`agent-mesh daemon [-config <file>] [-print-load]` reads `<config-dir>/daemon.toml` by default; unknown keys are rejected. `-print-load` prints hardware, slots and current load as JSON and exits (no hub needed).

| Key | Default | Meaning |
|---|---|---|
| `hub_url` | `$AGENTMESH_URL`, else `client.toml`, else `http://127.0.0.1:8787` | hub address |
| `host_token_file` | `host.token` next to `daemon.toml` | daemon token (0600 required) |
| `state_dir` | `~/.local/share/agent-mesh` | `repos/ worktrees/ logs/ spool/ run/` (0700) |
| `poll_interval`, `heartbeat_interval` | `30s`, `60s` | claim tick, lease heartbeat |
| `task_timeout` | `3h` | wall clock per worker; then killed, task stuck (`timeout`) |
| `git_call_timeout` | `120s` | per git/gh call; keep below the hub's `lease_ttl / 2` |
| `max_slots` | unset (derived: `clamp(min(cores/4, (ram_gb-8)/4), 1, 4)`) | concurrent tasks |
| `max_cpu_pct`, `min_mem_avail_pct` | `80`, `25` | checked before each claim |
| `max_agent_worktrees` | `6` | counts only `state_dir/worktrees/*` (kept stuck worktrees included) |
| `stuck_retention`, `log_retention` | `72h`, `168h` | GC of stuck worktrees and worker logs (at start and daily) |
| `allowed_permission_modes` | `["acceptEdits", "dontAsk"]` | `plan` also allowed; `bypassPermissions` / `auto` refuse to start |
| `max_budget_usd_cap` | `5` | tasks above it are not claimed |
| `worker_setting_sources` | `"project"` | `--setting-sources` for workers; add `user` to load user settings |
| `claude_bin`, `gh_bin`, `gitleaks_bin` | `claude`, `gh`, `gitleaks` | resolved on the service PATH |
| `claude_env_file` | `""` | optional 0600 env file for workers, e.g. `CLAUDE_CODE_OAUTH_TOKEN=...` from `claude setup-token` |
| `gh_env_file` | `gh.env` next to `daemon.toml` | 0600 env file for git/gh only, e.g. `GH_TOKEN=<fine-grained PAT>`; a missing file means gh uses its own login |
| `[repos]` | empty | allowlist: repo key -> remote URL the daemon clones. Keys are compared canonically; use `"github.com/<user>/<repo>"` so tasks added from sessions match |
| `[tool_profiles.<name>]` `allow = [...]` | none | tool allow list for tasks naming that profile (default profile name: `default`). Bare `Bash` / `Bash(*)` is refused |

Env files must not be group/world readable. Worker env never contains `GH_TOKEN`, `GITHUB_TOKEN`, `GH_ENTERPRISE_TOKEN` or `AGENTMESH_TOKEN`.

## Manual setup

The rest of this guide describes what the commands automate and is the reference for manual installs.

### Layout installed by `deploy/install.sh <hub|daemon|both> [binary]`

| Path | Purpose |
|---|---|
| `~/.local/bin/agent-mesh` | binary (default source `dist/agent-mesh-<os>-<arch>`, so run `make cross` first) |
| `<config-dir>` (0700) | `hub.toml`, `daemon.toml` copied from `deploy/config/*.example.toml` when absent |
| `~/.local/share/agent-mesh/` (0700) | `hub.db`, daemon `repos/ worktrees/ logs/ spool/ run/` |
| `~/.config/systemd/user/agent-mesh-{hub,daemon}.service` | Linux units |
| `~/Library/LaunchAgents/com.agentmesh.daemon.plist`, `~/Library/Logs/agent-mesh/` | macOS daemon (hub on Linux only) |

The script never overwrites existing configs, never creates secrets, never starts services, and never touches `~/.claude*` or your working clones. It prints the remaining steps.

### Ubuntu

1. `make test cross && deploy/install.sh both`.
2. Tokens, as printed by the script: `agent-mesh token new --admin --out <config-dir>/admin.token`, then `--host <host-name> --out <config-dir>/host.token` and `--session <host-name> --out <config-dir>/session.token`. Paste each printed `sha256:` hash into `hub.toml` (`admin_token_hash`, `[host_tokens]`, `[session_tokens]`). For another host, create its tokens the same way and copy the token files to its `<config-dir>`.
3. `gh.env`: `GH_TOKEN=<fine-grained PAT>` (contents rw, pull requests rw, allowlisted repos only), mode 0600. Optional `claude.env` with `CLAUDE_CODE_OAUTH_TOKEN=...`, referenced by `claude_env_file`.
4. Edit `daemon.toml` `[repos]` and tool profiles (see [Daemon config](#daemon-config)).
5. `loginctl enable-linger $USER` so units run without login.
6. Network must be up at boot for GitHub: `nmcli connection modify <connection-name> connection.autoconnect yes`.
7. `systemctl --user daemon-reload && systemctl --user enable --now agent-mesh-hub agent-mesh-daemon`.
8. Verify: `ss -ltnp | grep 8787` shows `127.0.0.1`; `agent-mesh hosts` lists the host without `unhealthy`; `agent-mesh doctor`.
9. Reboot test: reboot without logging in; both units active, `agent-mesh hosts` healthy.

Units use `Restart=on-failure`, `RestartSec=10`, `StartLimitIntervalSec=0` so a late network never exhausts the restart budget. The daemon unit sets an explicit `PATH` (`~/.local/bin`, `~/go/bin`, `/usr/local/bin`, `/usr/bin`, `/bin`; systemd does not read your shell profile) and `KillMode=mixed` with `TimeoutStopSec=60` so it releases leases on stop. Validate edits with `systemd-analyze --user verify <unit>`.

### Admin token for the CLI

`report`, `task cancel` and `task requeue` read `<config-dir>/admin.token` automatically on the hub machine. Keep the admin token out of shell-global env. This repo's `.mcp.json` expects the session token in `AGENTMESH_SESSION_TOKEN` (for example `export AGENTMESH_SESSION_TOKEN="$(cat ~/.config/agent-mesh/session.token)"` in a gitignored `.envrc`); the hub refuses admin and daemon tokens on session endpoints.

## Optional: Tailscale and a second host (manual)

1. Install Tailscale on both hosts, same tailnet; note the hub's tailnet IP or MagicDNS name.
2. Hub: `agent-mesh up --listen <tailnet-ip>:8787` (or set `listen` in `hub.toml` and restart the hub). The hub's own `daemon.toml` / `client.toml` `hub_url` must point to the same address. The hub retries bind, so tailscaled start order is fine.
3. Optional firewall: `sudo ufw allow in on tailscale0 to any port 8787 proto tcp` (run by you, not by the scripts).
4. Mac, fast path: `agent-mesh join <hub-url> <bundle>` from the hub's `invite`. Manual path: `deploy/install.sh daemon` (substitutes your home into the plist, creates `~/Library/Logs/agent-mesh/`); edit `daemon.toml` (`hub_url = "http://<hub-tailnet-name>:8787"`); copy `host.token` and `session.token` into `<config-dir>`; create `gh.env`; then `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.agentmesh.daemon.plist`. Stop with `launchctl bootout gui/$(id -u)/com.agentmesh.daemon`.
5. On each host, `agent-mesh setup-claude`, then test a message in both directions.

## Claude integration check

```sh
sha256sum ~/.claude/settings.json          # macOS: shasum -a 256
agent-mesh setup-claude --dry-run
agent-mesh setup-claude
# restart sessions; /mcp lists agentmesh
agent-mesh setup-claude --uninstall        # settings.json bytes match the first checksum
```

## Backup and restore (manual)

```sh
umask 077; sqlite3 ~/.local/share/agent-mesh/hub.db ".backup <backup-path>"
```

Restore: stop the hub, replace `hub.db` (delete `hub.db-wal` and `hub.db-shm`), start the hub. Tasks restored in `running` state belong to leases that may no longer exist: cancel them (`agent-mesh task cancel <id>`) and requeue the ones you still want (`agent-mesh task requeue <id>`).

## Rollback

```sh
agent-mesh setup-claude --uninstall
systemctl --user disable --now agent-mesh-daemon agent-mesh-hub
launchctl bootout gui/$(id -u)/com.agentmesh.daemon   # macOS
rm ~/.local/bin/agent-mesh
```

Config and data directories are kept for inspection.
