# Final delivery summary (lead)

Date: 2026-10-16 · main pushed at `2497b22` (CI green on Linux + macOS) · no release/tag created (user decision pending)

## What exists
- Hub (Go, SQLite WAL, REST + MCP) with fenced leases, idempotent claim/add/finish, reaper, messaging inbox with long-poll wake + ack.
- Interactive sessions as workers: register (repo normalized from git remote), targeted tasks (host/session), idle wake via background waiter, claim → work → `task_done`, creator notified. Opt-out `AGENTMESH_WORKER=off`.
- `setup-claude`: user-scope MCP (headersHelper, no token in files) + managed hooks; byte-identical uninstall; never touches unmanaged entries.
- Onboarding: `up`, `invite`, `join` (code via hidden stdin), `doctor`, `sessions`, `hosts`, `report`.
- Optional overnight daemon: isolated `claude -p` per task, edit-only default profile, deny floor, gitleaks, journaled push/PR.
- Distribution: CI, tag-triggered release workflow, `curl … | sh` installer, `go install`.

## Evidence
| Check | Result |
|---|---|
| `go test -race ./...` (14 pkgs, incl. e2e two-host + friend acceptance) | pass |
| Live real-Claude sessions (2 runs) | idle wake 3-6 s, cross-host task end-to-end ~24 s, worker:off respected, request_id replay, /exit + kill -9 cleanup |
| Soak 30 sessions, 3 min, SIGTERM + 2×SIGKILL hub, real daemons | 0 violations, 1850 tasks, 181 PRs one per task, DB integrity ok |
| Old DB files v1/v3/v5 upgraded by current code | pass |
| Reviews: plan red-team (15), hub, CLI/MCP/onboard, daemon (×2), final whole-repo | all Critical/High fixed with regression tests |
| gitleaks full history; infra-detail grep over full history | clean / 0 matches |
| GitHub CI on `2497b22`: vet + race tests on ubuntu-latest AND macos-latest, cross-build, shellcheck, gitleaks | all green (first real-Mac run found test path assumptions + a `ps` PATH issue; both fixed) |

## Needs the user
1. Tailscale on both machines (multi-host transport); then `agent-mesh up --listen <tailscale-ip>:8787`.
2. Mac run of the checklist in docs/deployment-guide.md (launchd, keychain).
3. Ubuntu reboot-without-login check (linger, wifi autoconnect).
4. Decide whether to tag `v0.1.0` (creates a public GitHub Release; installer one-liner needs it).

## Known limits / accepted trade-offs
- Auto-claim by bypassPermissions sessions runs tasks from your other sessions unattended (user goal); mitigated by framing, worker opt-out, token-only creators.
- Session workers ignore daemon tool profiles/budgets.
- Fully idle session without its waiter (never prompted once) wakes only at next prompt.
- Single hub; no HA.

## Unresolved questions
- None blocking; items above are user actions.
