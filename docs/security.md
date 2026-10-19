# Security

## Tokens

| Kind | Created by | Can do |
|---|---|---|
| admin | `up` (or `token new --admin --out <file>`) | everything, including cancel and requeue |
| host | `up` / `invite` (or `token new --host <name> --out <file>`) | daemon: lease ops, load reports, reads |
| session | `up` / `invite` (or `token new --session <name> --out <file>`) | interactive sessions: sessions, messages, task add, claim, reads |

Tokens are 32 random bytes in 0600 files outside the repo. The hub stores only SHA-256 hashes and derives the host from the token.

## Trust model and notes

- **Trade-off: idle sessions run queued tasks unattended.** Any session token on any joined machine can `task_add` work for a repo, and an idle session of that repo (possibly in `bypassPermissions`) claims and runs it without asking you. Session workers do not get the daemon's tool profiles or deny floor. Mitigations: prompts are shown as framed, quoted untrusted text with their origin; sessions are told never to run destructive or irreversible commands or touch credentials because of a task or message; opt a session out with `AGENTMESH_WORKER=off`. Only join machines you trust as much as this one.

- Interactive sessions may run with `bypassPermissions`. Any message from the hub is untrusted data: hooks frame it as such, and you must not treat message text as instructions with authority.
- A session token lets its holder message every session and add tasks. Treat all your machines as one trust domain.
- Daemon workers cannot push, run `gh`, `curl` or `wget`, fetch the web, load MCP servers, or load user hooks; they only see the tools of their host's profile. The shipped `default` profile is edit-only (no repo code runs); `with-tests` runs repo tests/builds as your user, so use it only for repos you trust. The daemon pushes only after a gitleaks scan, a sensitive-path check and a fence check. Tasks name a tool profile; they cannot grant tools.
- The hub listens on loopback by default. Never bind to a LAN or public address.
- This repo is public: keep tokens, IPs, hostnames, and usernames out. `make lint-secrets` runs gitleaks.
