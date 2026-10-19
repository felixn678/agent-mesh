# Troubleshooting

- `agent-mesh hosts` shows `throttled: unhealthy: <err>`: the daemon's preflight or two task setups in a row failed. Check the service log (see [Where things live](usage-guide.md#where-things-live)); usual causes are `claude`, `gh`, or `gitleaks` missing from the service PATH, or an expired credential. It re-checks every 10 minutes.
- `throttled: slots_full`, `cpu 85%`, `mem 12%`, `worktrees`: the daemon is busy or the machine is loaded; it claims again when the check clears. `agent-mesh daemon --print-load` shows the computed slots and load.
- Task stays queued: no eligible taker. Check that the task's repo matches a daemon `[repos]` key or an idle session's repo, its tool profile exists on the host, and its budget is within `max_budget_usd_cap`.
- Services dead after reboot (Linux): check `loginctl show-user "$USER" | grep Linger` and the network autoconnect setting.
- Hub bind fails: another process holds the port, or the Tailscale address is not up yet (the hub retries for 2 minutes).
- Hook silent: expected when the hub is unreachable; check `agent-mesh doctor`.
- `agent-mesh hosts` says `no daemon has reported load yet`: only daemons report load. It still names the machines that have live Claude sessions; `agent-mesh sessions` lists them. Start the daemon (service, or `agent-mesh daemon`) if you want load shown.
- `task cancel` / `task requeue` → `needs the admin token`: run them on the hub machine (only it has `admin.token`).
- `task add` → `--budget <usd> is required`: every task carries a spending cap (for example `--budget 2`, at most 100).
- `doctor` warns `service hub: not running` while the hub works: you started it by hand (`up --no-service` + `agent-mesh hub`); the warning is about the service only. `hub reachable` is what matters.
- Hooks do nothing after moving the binary: hooks and services store its absolute path. Re-run `agent-mesh setup-claude` (and `up`/`join` for services). Keep the binary in a directory without spaces.

## Limits

v1 is tested with the hub on Ubuntu (`up` also installs a launchd hub on macOS; `deploy/install.sh` installs a hub on Linux only). No hub while that machine is off: daemons back off and leases get grace when it returns. Manual backup only. Multi-machine traffic is plaintext HTTP and is only supported over Tailscale. Idle wake is best effort.

Non-goals: no remote exec, no SSH in code, no TLS or mDNS code, no tmux, no hub HA, no task priorities, no group messages.
