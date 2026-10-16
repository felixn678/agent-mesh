# Deployment guide

All names in angle brackets are placeholders. Never commit real values.

## Prerequisites (per host)

git, gh, claude (logged in), gitleaks (`go install github.com/zricethezav/gitleaks/v8@<pinned>` or a release binary). Build artifacts: `make cross` writes `dist/agent-mesh-{linux-amd64,darwin-arm64,darwin-amd64}`.

## Fast path: `up`, `invite`, `join`, `doctor`

| Command | Where | What it does |
|---|---|---|
| `agent-mesh up [--listen addr] [--name host] [--no-service]` | hub machine | creates the config dir (0700), `admin.token`, `host.token`, `session.token` (0600), `hub.toml`, `daemon.toml`, `client.toml`; installs and starts the hub and daemon user services (systemd `--user` on Linux, launchd on macOS). Idempotent. `--listen` on an existing setup rewrites `listen` and `hub_url` and restarts the hub. |
| `agent-mesh invite <host-name> [--url u]` | hub machine | mints host and session tokens, appends their hashes to `hub.toml` (comments preserved, file validated before it replaces the old one), signals the hub to reload, prints `agent-mesh join <hub-url> <bundle>`. |
| `agent-mesh join <hub-url> <bundle> [--no-service]` | other machine | verifies both tokens against the hub, then writes `host.token`, `session.token`, `daemon.toml`, `client.toml` and installs the daemon service. |
| `agent-mesh doctor` | any | checks config, token modes, hub reachability, auth, `claude auth status`, git, `gh auth status`, gitleaks, services, Tailscale. Exit status is non-zero if a required check fails. |

The bundle is base64url of `{"v":1,"url","host","host_token","session_token"}`; treat it as a secret. The hub reloads its token tables (admin, host, session) on `SIGHUP`; a `hub.toml` that fails validation is rejected and the old tokens stay active. Listen address, database and timings still need a restart. While running, the hub writes `hub.pid` next to `hub.toml` so `invite` can signal it when no service manager owns the hub. `client.toml` holds `hub_url` for CLI commands (`AGENTMESH_URL` overrides it).

`up` binds loopback unless you pass `--listen`, and refuses wildcard addresses. Services run the binary at the path you launched `up`/`join` from (resolved), so install `agent-mesh` to a stable location first (for example `~/.local/bin`). Unit files are rewritten only when their content changes, and only then is the service restarted.

## Manual setup

The rest of this guide describes what the commands automate and is the reference for manual installs.

## Layout installed by `deploy/install.sh <hub|daemon|both> [binary]`

| Path | Purpose |
|---|---|
| `~/.local/bin/agent-mesh` | binary |
| `~/.config/agent-mesh/` (0700) | `hub.toml`, `daemon.toml`, tokens, `gh.env` (all 0600) |
| `~/.local/share/agent-mesh/` (0700) | `hub.db`, daemon `repos/ worktrees/ logs/ spool/ run/` |
| `~/.config/systemd/user/agent-mesh-*.service` | Linux units |
| `~/Library/LaunchAgents/com.agentmesh.daemon.plist` | macOS daemon |

The script never overwrites existing configs, never creates secrets, never starts services, and never touches `~/.claude*` or your working clones.

## Ubuntu (v1)

1. `make test cross && deploy/install.sh both`.
2. Tokens (see README quick start). Paste the printed hash lines into `hub.toml`. Copy `host.token` and `session.token` of another host to it manually.
3. `gh.env`: `GH_TOKEN=<fine-grained PAT>` (contents rw, pull requests rw, allowlisted repos only), mode 0600. Optional `claude.env` with `CLAUDE_CODE_OAUTH_TOKEN=...` from `claude setup-token`, referenced by `claude_env_file`.
4. Edit `daemon.toml` `[repos]` and tool profiles.
5. `loginctl enable-linger $USER` so units run without login.
6. Network must be up at boot for GitHub: `nmcli connection modify <connection-name> connection.autoconnect yes`.
7. `systemctl --user daemon-reload && systemctl --user enable --now agent-mesh-hub agent-mesh-daemon`.
8. Verify: `ss -ltnp | grep 8787` shows `127.0.0.1`; `agent-mesh hosts` lists the host healthy.
9. Reboot test: reboot without logging in; both units active, `agent-mesh hosts` healthy.

Units use `Restart=on-failure`, `RestartSec=10`, `StartLimitIntervalSec=0` so a late network never exhausts the restart budget. The daemon unit sets an explicit `PATH` (systemd does not read your shell profile) and `KillMode=mixed` with `TimeoutStopSec=60` so it releases leases on stop. Validate edits with `systemd-analyze --user verify <unit>`.

### Admin token for the CLI

Keep it out of shell-global env. Use a gitignored `.envrc` (direnv) in the agent-mesh checkout or a wrapper script that reads `~/.config/agent-mesh/admin.token`. Interactive sessions elsewhere use the host's session token via `setup-claude`. This repo's `.mcp.json` expects the session token in `AGENTMESH_SESSION_TOKEN` (for example `export AGENTMESH_SESSION_TOKEN="$(cat ~/.config/agent-mesh/session.token)"` in the same `.envrc`); the hub refuses admin and daemon tokens on session endpoints.

## Optional: Tailscale and a second host (manual)

1. Install Tailscale on both hosts, same tailnet; note the hub's tailnet IP or MagicDNS name.
2. Hub: `listen = "<tailnet-ip>:8787"`. Ubuntu daemon `hub_url` to the same address. The hub retries bind so tailscaled start order is fine.
3. Optional firewall: `sudo ufw allow in on tailscale0 to any port 8787 proto tcp` (run by you, not by the scripts).
4. Mac: `deploy/install.sh daemon` (it substitutes your home into the plist and creates `~/Library/Logs/agent-mesh/`); edit `daemon.toml` (`hub_url = "http://<hub-tailnet-name>:8787"`); copy `host.token`, `session.token`, and create `gh.env`.
5. `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.agentmesh.daemon.plist`. Stop with `launchctl bootout gui/$(id -u)/com.agentmesh.daemon`.
6. On each host, `agent-mesh setup-claude`, then test a message in both directions.

## Claude integration check

```sh
sha256sum ~/.claude/settings.json
agent-mesh setup-claude --dry-run
agent-mesh setup-claude
# restart sessions; /mcp lists agentmesh
agent-mesh setup-claude --uninstall   # settings.json bytes match the first checksum
```

## Backup and restore (manual)

```sh
umask 077; sqlite3 ~/.local/share/agent-mesh/hub.db ".backup <backup-path>"
```

Restore: stop the hub, replace `hub.db` (delete `hub.db-wal` and `hub.db-shm`), start the hub. Tasks restored in `running` state belong to leases that may no longer exist: mark them stuck with reason "restored" and requeue as needed.

## Rollback

```sh
agent-mesh setup-claude --uninstall
systemctl --user disable --now agent-mesh-daemon agent-mesh-hub
launchctl bootout gui/$(id -u)/com.agentmesh.daemon   # macOS
rm ~/.local/bin/agent-mesh
```

Config and data directories are kept for inspection.
