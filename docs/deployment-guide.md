# Deployment guide

All names in angle brackets are placeholders. Never commit real values.

`<config-dir>` below is `~/.config/agent-mesh` on Linux (`$XDG_CONFIG_HOME/agent-mesh` if set) and `~/Library/Application Support/agent-mesh` on macOS. Data lives in `~/.local/share/agent-mesh` on both.

## Prerequisites (per host)

git, gh, claude (logged in), gitleaks (`go install github.com/zricethezav/gitleaks/v8@<pinned>` or a release binary). Binary: the one-line installer or `go install` (see [usage-guide.md](usage-guide.md#install)), or build with Go 1.26: `make build` writes `bin/agent-mesh`; `make cross` writes `dist/agent-mesh-{linux-amd64,linux-arm64,darwin-arm64,darwin-amd64}`. Install the binary to a stable path (for example `~/.local/bin/agent-mesh`) before `up`, `join` or `setup-claude`: services and Claude hooks call it by its resolved absolute path.

## Fast path: `up`, `invite`, `join`, `doctor`

| Command | Where | What it does |
|---|---|---|
| `agent-mesh up [--listen addr] [--name host] [--daemon] [--no-service] [--no-claude]` | hub machine | creates `<config-dir>` (0700), `admin.token`, `host.token`, `session.token` (0600), `hub.toml`, `daemon.toml`, `client.toml`; installs and starts the hub user service (systemd `--user` on Linux, launchd on macOS), plus the daemon service with `--daemon` or when it is already installed, with a PATH that includes the directories of claude, git, gh and gitleaks. Idempotent. A first run with Tailscale connected listens on the tailnet address. `--listen` on an existing setup rewrites `listen` and `hub_url` and restarts the hub (and daemon). Ends by running `setup-claude` unless `--no-claude`. |
| `agent-mesh invite <host-name> [--url u]` | hub machine | mints host and session tokens, appends their hashes to `hub.toml` (comments preserved, file validated before it replaces the old one), signals the hub to reload (only after checking the pid really is an agent-mesh hub), prints `agent-mesh join` plus the code to paste when asked (the code carries the hub URL). Warns when the URL is loopback. |
| `agent-mesh join [--daemon] [--no-service] [--no-claude]` (code read from stdin; `join <code>` and `join <hub-url> <code>` also accepted, the URL overriding the code's) | other machine | verifies both tokens against the hub (`/api/v1/whoami`: host token must be kind host, session token kind session), then writes `host.token`, `session.token`, `daemon.toml`, `client.toml`; installs the daemon service with `--daemon` or when it is already installed (then restarted if the hub URL changed). Ends by running `setup-claude` unless `--no-claude`. |
| `agent-mesh daemon install` | any joined machine | adds the optional daemon service later. |
| `agent-mesh doctor` | any | checks config dir, token modes, hub reachability, each token's kind and auth, `claude auth status`, git, and where the daemon is installed `gh auth status`, gitleaks and that its service PATH contains claude; services, Tailscale. Exit status is non-zero if a required check fails (services and Tailscale are warnings only). |

The bundle is base64url of `{"v":1,"url","host","host_token","session_token"}`; treat it as a secret. The hub reloads its token tables (admin, host, session) on `SIGHUP`; a `hub.toml` that fails validation is rejected and the old tokens stay active. Listen address, database and timings still need a restart. While running, the hub writes `hub.pid` next to `hub.toml` so `invite` can signal it when no service manager owns the hub. `client.toml` holds `hub_url` for CLI commands (`AGENTMESH_URL` overrides it).

`up` binds loopback unless you pass `--listen` or Tailscale is connected on its first run, and refuses wildcard addresses. Unit files are rewritten only when their content changes, and only then is the service restarted. With `--no-service`, run `agent-mesh hub` (and `agent-mesh daemon`) yourself.

After `daemon install` (or `--daemon`), a daemon has no repos yet: edit `[repos]` in `daemon.toml` and provide `gh.env` (below), then restart the daemon service.

## Daemon config

`agent-mesh daemon [-config <file>] [-print-load]` reads `<config-dir>/daemon.toml` by default; unknown keys are rejected. `-print-load` prints hardware, slots and current load as JSON and exits (no hub needed).

| Key | Default | Meaning |
|---|---|---|
| `hub_url` | `$AGENTMESH_URL`, else `client.toml`, else `http://127.0.0.1:47878` | hub address |
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
| `[tool_profiles.<name>]` `allow_ssh_agent` | `false` | pass `SSH_AUTH_SOCK` / `SSH_AGENT_PID` to workers of this profile |

Generated configs ship two profiles. `default` is edit-only (Read/Edit/Write/Glob/Grep plus `git status|diff|log|add|commit`): workers run no repo code. `with-tests` adds test/build commands and therefore **runs repo code as your user**, outside the worker's deny list (that list only binds Claude's own tools), with access to your files and tokens: use it only for repos you trust, via `--profile with-tests`.

Env files must not be group/world readable. Worker env never contains `GH_TOKEN`, `GITHUB_TOKEN`, `GH_ENTERPRISE_TOKEN`, any `AGENTMESH_*` variable (except `AGENTMESH_HOOKS=off`), or the ssh agent (unless the profile sets `allow_ssh_agent`).

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
8. Verify: `ss -ltnp | grep 47878` shows `127.0.0.1`; `agent-mesh hosts` lists the host without `unhealthy`; `agent-mesh doctor`.
9. Reboot test: reboot without logging in; both units active, `agent-mesh hosts` healthy.

Units use `Restart=on-failure`, `RestartSec=10`, `StartLimitIntervalSec=0` so a late network never exhausts the restart budget. The daemon unit sets an explicit `PATH` (`~/.local/bin`, `~/go/bin`, `/usr/local/bin`, `/usr/bin`, `/bin`; systemd does not read your shell profile) and `KillMode=mixed` with `TimeoutStopSec=60` so it releases leases on stop. Validate edits with `systemd-analyze --user verify <unit>`.

### macOS

Where things live (note the space in the config path: quote it in shell commands):

| What | Path |
|---|---|
| Config, tokens | `~/Library/Application Support/agent-mesh/` (0700; files 0600) |
| Daemon state (clones, worktrees, journals) | `~/.local/share/agent-mesh/` |
| Service definitions | `~/Library/LaunchAgents/com.agentmesh.daemon.plist` (and `com.agentmesh.hub.plist` if the hub runs on the Mac) |
| Service logs | `~/Library/Logs/agent-mesh/daemon.log`, `hub.log` (written by the process, rotated at 10 MB, 3 old files kept); `*.stderr.log` holds only startup failures |

To use another directory (on any OS), set `AGENTMESH_CONFIG_DIR` to an absolute path before `up`/`join` and export it in your shell profile; the installed services get it in their environment.

Things that differ from Linux:

- **Services need a desktop login.** LaunchAgents run in your `gui/<uid>` launchd domain, which exists only while you are logged in to the Mac (it keeps running with the screen locked). `up`/`join` over a plain SSH session fail with a clear message; log in on the Mac once, or pass `--no-service`. Services start at login, not at boot.
- **Sleep stops work.** A sleeping Mac stops its daemon and sessions; leases expire and the hub hands the work to another machine. For overnight work on power: System Settings → Battery/Energy → "Prevent automatic sleeping when the display is off", or run `caffeinate -s` in a terminal. Closing the lid sleeps the Mac unless it is on power with an external display.
- **Claude login for the daemon.** `claude` keeps its login in the login Keychain, which a LaunchAgent can normally read after you log in. If `daemon.log` shows "not logged in", run `claude setup-token` in Terminal, put `CLAUDE_CODE_OAUTH_TOKEN=<token>` in a 0600 file (for example `~/Library/Application Support/agent-mesh/claude.env`) and set `claude_env_file` to it in `daemon.toml`. The token is long-lived: treat it like a password.
- **GitHub login for the daemon.** Prefer `gh.env` with `GH_TOKEN=<fine-grained PAT>` next to `daemon.toml`; a Keychain-backed `gh` login can prompt for access, which nobody answers at night.
- **Logs.** launchd never rotates `StandardOutPath`, so the LaunchAgents run `agent-mesh daemon|hub --log-file ~/Library/Logs/agent-mesh/<name>.log`: the process rotates its own log at 10 MB and keeps `.1`..`.3`. The `<name>.stderr.log` that launchd writes only collects startup failures and is emptied by the process once it passes 1 MB. On Linux, systemd sends output to the journal, which journald rotates (`journalctl --user -u agent-mesh-daemon`). `--log-file` works for manual runs too.
- **Apple Silicon:** use the arm64 build (the installer picks it even from a Rosetta terminal; check with `file "$(command -v agent-mesh)"`).
- Control the service: `launchctl print gui/$(id -u)/com.agentmesh.daemon | grep state`, restart `launchctl kickstart -k gui/$(id -u)/com.agentmesh.daemon`, stop `launchctl bootout gui/$(id -u)/com.agentmesh.daemon`.

#### Mac manual test checklist

Run once after joining the Mac; each step says what "good" looks like.

1. `agent-mesh version` prints the release; `file "$(command -v agent-mesh)"` says `arm64` on Apple Silicon.
2. On the hub: `agent-mesh invite <mac-name>`. On the Mac: `agent-mesh join --daemon` and paste the code (it is not echoed) → join reports both tokens verified.
3. `launchctl print gui/$(id -u)/com.agentmesh.daemon | grep state` → `state = running`.
4. `agent-mesh daemon --print-load` → sensible cores/RAM/CPU model (for example `Apple M2`) and slots (8 GB RAM → 1 slot).
5. `agent-mesh doctor` → all ✓ (hub reachable, session/host token kinds, `claude auth status`, `gh auth status`, service PATH contains claude).
6. On the hub: `agent-mesh hosts` lists the Mac with its hardware line and no `unhealthy`.
7. Re-run `agent-mesh join` with a fresh invite → no duplicate service (`launchctl list | grep agentmesh` shows one daemon).
8. `shasum -a 256 ~/.claude/settings.json`, then `agent-mesh setup-claude --dry-run`, `agent-mesh setup-claude`; open Claude in a project repo: the `sessions` tool lists sessions on both machines; send a message each way.
9. From a session on the Ubuntu machine: queue a task targeted at the Mac in the same repo ("add a task for `<mac-name>`: create hello.txt with hi and commit"). An idle Mac session in that repo wakes, does it and the requester gets the result.
10. Crash test: while a daemon task runs, `launchctl kill SIGKILL gui/$(id -u)/com.agentmesh.daemon` → launchd restarts it within ~10 s; the orphaned `claude` is killed and the task released (`agent-mesh task get <id>`).
11. Sleep test: start a long task on the Mac, sleep the Mac for 10 minutes, wake it → the task finishes on the Mac or on another machine, never twice (one PR at most).
12. Reboot, log in, wait a minute → step 3 again shows running.
13. `agent-mesh setup-claude --uninstall` → `shasum -a 256 ~/.claude/settings.json` matches step 8.

### Admin token for the CLI

`report`, `task cancel` and `task requeue` read `<config-dir>/admin.token` automatically on the hub machine. Keep the admin token out of shell-global env. The MCP server Claude uses comes from `setup-claude` (user scope; its `headersHelper` reads `session.token`), so no repo needs its own `.mcp.json`; the hub refuses admin and daemon tokens on session endpoints.

## Optional: Tailscale and a second host (manual)

1. Install Tailscale on both hosts, same tailnet; note the hub's tailnet IP or MagicDNS name.
2. Hub: `agent-mesh up --listen <tailnet-ip>:47878` (or set `listen` in `hub.toml` and restart the hub). The hub's own `daemon.toml` / `client.toml` `hub_url` must point to the same address. The hub retries bind, so tailscaled start order is fine.
3. Optional firewall: `sudo ufw allow in on tailscale0 to any port 47878 proto tcp` (run by you, not by the scripts).
4. Mac, fast path: `agent-mesh join --daemon` and paste the code printed by the hub's `invite` (see [macOS](#macos)). Manual path: `deploy/install.sh daemon` (substitutes your home into the plist, creates `~/Library/Logs/agent-mesh/`); edit `daemon.toml` (`hub_url = "http://<hub-tailnet-name>:47878"`); copy `host.token` and `session.token` into `<config-dir>`; create `gh.env`; then `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.agentmesh.daemon.plist`. Stop with `launchctl bootout gui/$(id -u)/com.agentmesh.daemon`.
5. On each host, `agent-mesh setup-claude`, then test a message in both directions.

## Claude integration check

```sh
sha256sum ~/.claude/settings.json          # macOS: shasum -a 256
agent-mesh setup-claude --dry-run
agent-mesh setup-claude
# restart sessions; /mcp lists agentmesh
agent-mesh setup-claude --uninstall        # settings.json bytes match the first checksum
```

## Machine roles

After both machines have joined, describe them once so sessions can choose `target_host` themselves:

```sh
agent-mesh host-note "backend, API, web frontend"                       # on each machine, for itself
agent-mesh host-note --host <mac-host> "frontend, Expo; Xcode, adb"     # or all of them from the hub machine
agent-mesh host-note                                                    # check: notes, detected tools, live sessions
```

New sessions get the briefing once the mesh has two machines; sessions that are already open keep their old context until restarted.

## Backup and restore (manual)

```sh
umask 077; sqlite3 ~/.local/share/agent-mesh/hub.db ".backup <backup-path>"
```

Restore: stop the hub, replace `hub.db` (delete `hub.db-wal` and `hub.db-shm`), start the hub. Tasks restored in `running` state belong to leases that may no longer exist: cancel them (`agent-mesh task cancel <id>`) and requeue the ones you still want (`agent-mesh task requeue <id>`).

## Updating

`agent-mesh update [--version vX.Y.Z] [--check] [--no-restart]` on each machine, hub machine first.

- Source: `https://github.com/felixn678/agent-mesh/releases/latest/download/<asset>` (or `/releases/download/<tag>/` with `--version`), asset `agent-mesh-<os>-<arch>.tar.gz` plus `SHA256SUMS`, HTTPS only. `AGENTMESH_DOWNLOAD_BASE` points at a mirror directory instead (http:// and file:// allowed there), as for the installer.
- The archive must match `SHA256SUMS`; the unpacked binary must be built for this OS/CPU and report a release version. It is staged next to the installed binary, then renamed over it (atomic; running processes keep the old file).
- `--check` prints current and latest and changes nothing. It reads the latest tag from GitHub's `latest/download` redirect without downloading; a mirror without that redirect gets the release downloaded and its version read. Exit 0 when up to date (or newer), 10 when an update is available, 1 on errors.
- No release published yet (404 on `latest`): a message with the `go install ...@latest` alternative, exit 1.
- Same version: "already up to date". A latest release older than the running binary is not installed unless named with `--version` (the rollback path).
- Development builds (`dev` or a Go pseudo-version) need `--version`. Binaries under `$GOBIN`, `$GOPATH/bin` (default `~/go/bin`) or the Go build cache are left alone; `update` prints `go install github.com/felixn678/agent-mesh/cmd/agent-mesh@<tag>`. A binary in a directory you cannot write gets the installer command instead (installs to `~/.local/bin`).
- Afterwards the installed services whose unit runs this binary are restarted, hub then daemon (`systemctl --user restart` / `launchctl kickstart -k`); the daemon releases its leases on stop. Units that run another binary path are reported, not restarted. Hooks and the MCP server exec the binary per call and need nothing.

## Rollback

```sh
agent-mesh setup-claude --uninstall
systemctl --user disable --now agent-mesh-daemon agent-mesh-hub
launchctl bootout gui/$(id -u)/com.agentmesh.daemon   # macOS
rm ~/.local/bin/agent-mesh
```

Config and data directories are kept for inspection.
