---
phase: 5
title: "Install and ops"
status: pending
priority: P2
effort: "3h"
dependencies: [2, 3, 4]
---

# Phase 5: Install and ops

## Overview

Run unattended on Ubuntu (v1): cross-compiled binary, systemd user units (hub + daemon), configs, per-host/admin tokens and GH PAT outside the repo, loopback bind, reboot resilience, docs. Mac + Tailscale is an optional subsection tied to phase 3b. Repo is public: all examples use placeholders (`<user>`, `<hub-host>`, `<hub-tailscale-ip>`, `<host-name>`).

## Constraints
- Install touches only `~/.local/bin/agent-mesh`, `~/.config/agent-mesh/`, `~/.local/share/agent-mesh/`, `~/.config/systemd/user/agent-mesh-*`, `~/Library/LaunchAgents/com.agentmesh.daemon.plist` (Mac).
- `install.sh` **never** modifies `~/.claude/`, `~/.claude.json`, or the user's working clones. User-scope Claude integration is a separate, opt-in, per-host manual step: `agent-mesh setup-claude` (phase 2; decision A, 2026-10-16) — additive hooks + user-scope MCP; never touches ClaudeKit skills/rules/existing hooks.

## Requirements
- Build: `make build` → `bin/agent-mesh-linux-amd64`, `bin/agent-mesh-darwin-arm64` (`CGO_ENABLED=0`, `-ldflags "-X <module>/internal/version.Version=$(git describe --always)"`). `make test` = vet + `go test -race ./...`.
- Prereqs per host: git, gh, claude, gitleaks (`go install github.com/zricethezav/gitleaks/v8@<pinned>` or release binary).
- Secrets (all 0600 in `~/.config/agent-mesh/`, never in repo):
  - `agent-mesh token new --admin --out admin.token` → hash into hub.toml `admin_token_hash`.
  - `agent-mesh token new --host <host-name> --out host.token` per host → hash into `[host_tokens]`. Copy to remote host manually.
  - `gh.env`: `GH_TOKEN=<fine-grained PAT>` (contents rw + pull requests rw, allowlisted repos only).
  - optional `claude.env`: `CLAUDE_CODE_OAUTH_TOKEN=…` from `claude setup-token`.
- Admin token for interactive use: gitignored `.envrc` in the agent-mesh repo (direnv) or wrapper script — not shell-global. Interactive sessions elsewhere use the host's own `host.token` (decision C) via `setup-claude`.
- Ubuntu systemd **user** units + `loginctl enable-linger <user>`:
  - common: `[Unit] StartLimitIntervalSec=0`; `[Service] Restart=on-failure RestartSec=10 UMask=0077`.
  - `agent-mesh-hub.service`: `ExecStart=%h/.local/bin/agent-mesh hub`.
  - `agent-mesh-daemon.service`: `After=agent-mesh-hub.service`, `Environment=PATH=%h/.local/bin:%h/go/bin:/usr/local/bin:/usr/bin:/bin`, `TimeoutStopSec=60`, `KillMode=mixed` (SIGTERM to daemon first so it releases leases).
- Network v1: hub `listen = "127.0.0.1:8787"`, daemon `hub_url = "http://127.0.0.1:8787"`. No LAN exposure, no `.local` hostname, no TLS code.
- Backup: **deferred**. Document manual `umask 077; sqlite3 ~/.local/share/agent-mesh/hub.db ".backup <path>"` + restore runbook (mark restored `running` rows stuck "restored").
- Docs: `README.md` (what/why, constraints/non-goals, Ubuntu quickstart, add task, report, security model, troubleshooting), `docs/system-architecture.md` (components, data flow, lease/fence/idempotency protocol, publish journal, failure-modes table, token model, file layout).

## Architecture
```
v1 Ubuntu (<hub-host>)                         later: Mac (phase 3b)
 systemd --user (linger)                         launchd LaunchAgent
  agent-mesh hub    127.0.0.1:8787               agent-mesh daemon ──tailnet──► <hub-tailscale-ip>:8787
  agent-mesh daemon ──loopback──► hub              └ claude -p (own clones/worktrees)
   └ claude -p (own clones/worktrees)
```

### Optional: Tailscale + Mac (manual user steps; after 3b gate GO)
1. User installs Tailscale on both hosts, logs in to same tailnet; note hub's tailnet IP / MagicDNS name.
2. hub.toml `listen = "<hub-tailscale-ip>:8787"`; Ubuntu daemon `hub_url` → same; bind retry covers tailscaled start order.
3. Optional ufw: allow 8787 only on `tailscale0`.
4. Mac: `agent-mesh-darwin-arm64` → `~/.local/bin`; daemon.toml (`hub_url = "http://<hub-tailnet-name>:8787"`, slots auto-derived, `max_slots` only if needed); `host.token`, `gh.env`; LaunchAgent plist (`KeepAlive`, `RunAtLoad`, `EnvironmentVariables.PATH` incl. `/opt/homebrew/bin`, logs `~/Library/Logs/agent-mesh/`); `launchctl bootstrap gui/$(id -u) …`.
5. Verify go-sdk DNS-rebinding check accepts tailnet Host header (phase 2 risk).
6. Mac: `agent-mesh setup-claude` (same as Ubuntu step 7); cross-host test: session on Mac ↔ session on Ubuntu, both directions.

## Related Code Files
Create: `Makefile`, `deploy/systemd/agent-mesh-hub.service`, `deploy/systemd/agent-mesh-daemon.service`, `deploy/launchd/com.agentmesh.daemon.plist`, `deploy/config/hub.example.toml`, `deploy/config/daemon.ubuntu.example.toml`, `deploy/config/daemon.mac.example.toml`, `deploy/install.sh`, `README.md`, `docs/system-architecture.md`.
Modify: `.mcp.json` only if phase-2 step 1 chose the url fallback. No Go code (bugs → owning package with lead approval).

## Implementation Steps
1. Makefile + cross-compile; `file bin/*` arch check.
2. Example configs with placeholders only (`max_slots` commented out: auto from hardware); `git grep -nE '192\.168\.|10\.[0-9]+\.[0-9]+\.[0-9]+|[a-z0-9-]+\.local[:/]'` plus a grep for the real username/hostname → clean.
3. Units/plist; `systemd-analyze --user verify`, `plutil -lint` (on Mac).
4. `install.sh <hub|daemon>`: idempotent, copies binary/units, creates dirs 0700, never generates/copies secrets, never touches `~/.claude*`; prints manual steps (tokens, gh.env, enable-linger, wifi autoconnect).
5. **Manual user step**: ensure the Ubuntu network connection autoconnects at boot (`nmcli connection modify <connection-name> connection.autoconnect yes`), needed for GitHub reachability.
6. Ubuntu install → hub + daemon up → enqueue via MCP in agent-mesh repo → draft PR.
7. **Opt-in per host**: `sha256sum ~/.claude/settings.json ~/.claude.json` → `agent-mesh setup-claude --dry-run` (review diff) → `agent-mesh setup-claude` → restart Claude sessions → in `~/Code/<repo>` `/mcp` shows agentmesh; `sessions` lists itself; two local sessions exchange a message (turn end, next prompt, idle wake if spike passed). Uninstall check: `agent-mesh setup-claude --uninstall` → checksums equal the pre-setup values (claude may rewrite unrelated `~/.claude.json` fields; compare `mcpServers` + settings.json bytes).
8. **Reboot test**: reboot, no GUI login → both units active, preflight healthy, one task completes.
9. Ubuntu overnight soak: 6–10 small tasks; kill daemon mid-task once; restart hub mid-task once; report shows reasons and all PRs.
10. README + architecture doc (incl. messaging security note: messages are untrusted data, recipients may run bypassPermissions, tokens = same user's hosts only; delivery modes + limitation); every documented command actually run.

## Success Criteria
- [ ] Both binaries build `CGO_ENABLED=0`; `make test` green.
- [ ] Reboot without login → hub + daemon running (linger, RestartSec, no start limit), `agent-mesh hosts` shows host healthy.
- [ ] Soak: zero duplicate open PRs per task; every task done/stuck with reason; report < 1 s.
- [ ] `git grep` for token values, PAT prefix, private IPs, real hostnames/usernames in repo → empty.
- [ ] `ss -ltnp` shows hub on 127.0.0.1 (v1) or tailnet IP only.
- [ ] `~/.claude/`, `~/.claude.json`, user working clones unchanged by `install.sh` (checksum before/after).
- [ ] `setup-claude` → only agentmesh hook entries + `mcpServers.agentmesh` added (diff); `--uninstall` → settings.json byte-identical to pre-setup, `mcpServers.agentmesh` gone; ClaudeKit hooks still fire.

## Risk Assessment
| Risk | L×I | Mitigation |
|---|---|---|
| PATH under systemd misses claude/gh/gitleaks | H×M | explicit PATH; preflight marks unhealthy loudly |
| Network not up at boot | M×H | bind retry + RestartSec=10, StartLimitIntervalSec=0; autoconnect step; reboot test |
| Rogue hub / token sniffing on wifi | M×H | v1 loopback only; multi-host only over Tailscale |
| Infra details leak in public repo | M×M | placeholders; grep check in success criteria |
| Ubuntu off → no hub | M×M | accepted v1; daemons back off; lease grace on return |
| DB loss | L×H | WAL + synchronous=FULL; manual backup documented; timer deferred |
| setup-claude breaks ClaudeKit hooks | L×H | dry-run diff, backup `settings.json.agentmesh-bak`, uninstall byte-identical test (phase 2) |
| Rollback | — | `agent-mesh setup-claude --uninstall` per host (or restore `~/.claude/settings.json.agentmesh-bak` + `claude mcp remove -s user agentmesh`); `systemctl --user disable --now agent-mesh-hub agent-mesh-daemon`; `launchctl bootout gui/$(id -u)/com.agentmesh.daemon`; remove binary; config/data dirs retained for inspection |
