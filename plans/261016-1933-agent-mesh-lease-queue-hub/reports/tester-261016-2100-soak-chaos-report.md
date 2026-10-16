# Soak / chaos test: hub at user scale

Status: DONE_WITH_CONCERNS (all invariants hold in both runs; 2 design gaps reported, no code bug found in the hub)

## What runs
`internal/soak` (build tag `soak`; default `go test ./...` unaffected). Real built binary, temp HOMEs:
- hub machine: `up --no-service --listen 127.0.0.1:<free>`; hub.toml shortened: `lease_ttl`/`session_lease_ttl` 8s, `revive_window` 4s, `reap_interval` 1s, `stale_requeue` 30s (so dead sessions' leases expire inside the run); `agent-mesh hub` as a child process.
- second machine: `invite host-b` → `join --no-service`.
- 2 hosts × 3 repos × 5 sessions = 30 session actors (client package, each host's session token). Each loop: re-register every 3 s; during chaos 30 % task_add (1/3 untargeted, 1/3 `target_host`, 1/3 `target_session`, `created_by_session` = self), 30 % cross-host message; then one waiter round (`InboxWaitClaimable`, 3 s, no mark-read) → ack shown ids → claim with a fresh `request_id` when claimable → "work" 50-450 ms → heartbeat → complete 85 % / fail(retry) 10 % / release 5 %. 10 % of claims are re-sent with the same `request_id` (simulated lost response).
- deaths: 2 % per claim, max 8 of 30 sessions; a dying session holds its task; half come back 10-16 s later with a late complete.
- chaos: hub SIGTERM + restart at 25 %, 50 %, 75 %; `invite host-c` (hub.toml rewrite + SIGHUP) at 40 %, then host-c's new session token must work without restart.
- end: 30 s drain (no new adds/sends), stop actors, drain every live inbox (fetch → ack), SIGTERM hub, read the hub's SQLite file.

Run: `go test -tags soak -race -short ./internal/soak` (60 s chaos) / `go test -tags soak -timeout 15m ./internal/soak` (3 min); `SOAK_DURATION` overrides.

## Invariants checked
- one holder per (task, fence) across all claims (live check); same-request-id replay returns the same task+fence.
- every acknowledged task_add exists in the hub; no task completed twice; accepted complete fence == stored fence of the done task.
- end states: done / stuck (with reason) / queued (cause classified; queued with a live eligible taker = "stranded" = failure) / running only with an expiring lease.
- every acknowledged message exists; messages to live sessions delivered ≥ 1 and read; no message re-delivered after a successful ack; each session only receives its own messages.
- creator notices: every finished session-created task has a notice for its final state; no duplicate notice per (task, state); notices to live creators read.
- no HTTP 500 to any client; no ERROR/panic line in the hub log; clean exit (code 0) within 20 s of every SIGTERM.
- hub process FDs at end idle ≤ start idle + 10 (`/proc/<pid>/fd`), threads sampled.

## Results (final code)

| | race, short (60 s chaos) | full (3 min chaos) |
|---|---|---|
| wall (incl. 30 s drain) | 1m32s | 3m32s |
| tasks added / in hub | 785 / 785 | 2196 / 2196 |
| end states | done 740, stuck 1, queued 44 | done 2028, stuck 4, queued 164 |
| queued cause | all `target_session_dead` | all `target_session_dead` |
| stranded / running at end | 0 / 0 | 0 / 0 |
| finished tasks/s | 8.0 | 9.6 |
| (task,fence) claims / request-id replays | 872 / 86 | 2390 / 251 |
| claim latency p50 / p99 | 1.5 ms / 15.8 ms | 1.4 ms / 14.9 ms |
| add → first claim p50 / p99 | 3.6 ms / 417 ms | 3.7 ms / 370 ms |
| wake latency (add during wait → waiter returns claimable) p50 / p99 | 1.4 ms / 15.6 ms (n=588) | 1.7 ms / 15.8 ms (n=1620) |
| messages sent / to live sessions delivered+read | 819 / 586 | 2244 / 1638 |
| creator notices / finished tasks | 741 / 741 | 2032 / 2032 |
| late completes | accepted 1, lease_lost 1 | accepted 2, lease_lost 2 |
| HTTP 500 / hub log errors | 0 / 0 | 0 / 0 |
| SIGHUP reload | ok | ok |
| hub FDs start idle → loaded → end idle | 9 → 22-31 → 10 | 9 → 23-31 → 10 |
| hub threads | 8 → ~20 (stable) | 8 → ~21 (stable) |

Client errors are all transient around restarts (connection refused / reset, a few 503 `unavailable` on long polls during shutdown) and were retried; none were 500.

The add→claim p99 (~400 ms) is the actors' own work time (a busy session claims only after finishing its current task), not hub latency.

## Bugs found
- Hub: none. Every invariant held in all 7 runs after the harness fix below (3 race-short, 2 full 3 min, 2 shorter).
- Test harness only: my first hub.toml edit put `session_lease_ttl` into a comment, so sessions used the 30 min default and the end check flagged "running with lease 29 m in the future". Fixed in the harness; it confirmed the check works.

## Design gaps (not bugs in the frozen contract; owners: api/store/hub/mcptools)
1. `task_add` is not idempotent. A client that retries after a lost response (timeout after commit) creates a duplicate task. Not observed here (restart errors were connection refused = not committed), but MCP callers (Claude) retry on errors. Suggest an optional `request_id` on TaskSpec, like claims.
2. Tasks targeted at a session that dies stay `queued` forever (164 in the full run). The creator is never told. Suggest: reaper fails them as stuck (reason "target session gone") once the target session is ended or unseen past the alive window, which also triggers the creator notice.

## Not covered
- Daemons (`claude -p` workers) are not part of this soak; covered by internal/daemon and internal/e2e tests.
- Multi-machine network (Tailscale latency, partitions); everything is loopback.
- Hub crash (SIGKILL) rather than SIGTERM; WAL + `synchronous=FULL` should cover it but it was not exercised here.

## Unresolved questions
1. Should tasks targeted at a dead session expire (gap 2), and after how long?
2. Add `request_id` to task_add (gap 1) now or later?
