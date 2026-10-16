# Hub core adversarial review (commit f9e4209)

Scope: internal/{api,store,hub,client,config,hubtest}. Read-only review. #1 and #4 were reproduced with a throwaway test injected via `go test -overlay` (no repo files changed). Missing features ignored.

## Findings

### 1. High: final-attempt lease goes stuck on first late heartbeat; the worker's Fail is swallowed as `already_applied`
- `internal/store/lease_maint.go:28-31`: Reap marks `running` → `stuck` once `lease_expires_at < now` and `attempts >= max_attempts`. There is no ReviveWindow grace, unlike tasks below the cap (Heartbeat may revive those for 30 min).
- `internal/store/lease.go:151` + `:123`: the Fail okStates list includes `stuck`. A Fail from the same fence/host on a reaper-stuck task therefore returns 200 `already_applied=true` and writes nothing.
- Reproduced: max_attempts=1, claim, advance 5m02s, Reap.
  - Heartbeat → `lease_lost`.
  - Fail{cost 1.5, reason "secret scan blocked", worktree "/wt/1"} → `already=true err=nil`.
  - Task ends up `stuck`, reason "lease expired … at attempt cap", cost 0, worktree "".
  - A Complete would instead get `lease_lost`, so finished work (branch/PR) is discarded even though no other worker can hold the task: the fence is unchanged.
- Triggers:
  - laptop/host sleep or a network blip longer than TTL on any last attempt (every max_attempts=1 task);
  - two hub restarts within one ReapInterval: the second boot skips Grace (`graced >= alive`), so the first Reap kills every final-attempt lease that expired during the downtime.
- Fix:
  - Reap at cap only when `lease_expires_at < :now - :revive`, symmetric with Heartbeat.
  - Let the owner still finish a reaper-stuck task: on Complete/Fail, when state='stuck' and fence+host match, apply the update (guard `state IN ('running','stuck') AND fence=:fence AND claimed_by=:host`). Nobody else can own that fence.
  - At minimum, drop `stuck` from Fail's okStates unless the stored reason came from this worker.

### 2. Medium: Claim is not idempotent; a lost response orphans the task and burns an attempt
- `internal/store/lease.go:16-29,40-55` with `internal/client/client.go:22` (30 s CallTimeout).
- Scenario: the claim UPDATE commits (fsync with synchronous=FULL can be slow), then the daemon times out or the connection drops before reading the 200. The task is now `running` for a holder that doesn't know it has it, with attempts+1.
  - After TTL another claim reclaims it (another attempt used).
  - With max_attempts=1 it goes straight to `stuck` via #1 without ever running.
- Fix (either):
  - Daemon sends a claim id (UUID, retried on timeout); store it in a `claim_token` column and return the same row when `claimed_by=:host AND claim_token=:tok AND state='running'`.
  - Or on any claim error the daemon calls a `GET /tasks?state=running&claimed_by=me` reconcile and releases unknown tasks. The first is simpler and exact.

### 3. Medium: `mark_read` inbox is at-most-once; the 800 ms hook timeout loses messages silently
- `internal/store/sessions.go:170` marks messages read inside the request. The hook client has an 800 ms total timeout (phase-02 l.46) and fails open.
- Scenario: the hub is busy (single SQLite conn, FULL fsync, a 1000-row report query running) and the UPDATE…RETURNING commits at 700 ms. The response arrives after 800 ms, the hook gives up and exits 0. The messages are now read but were never shown to Claude, with no retry possible.
- Same for `InboxWait`: if the client gives up at wait+10 s after the server marked rows read, those messages are lost.
- Fix: two-phase delivery. GET returns unread messages without marking them; the hook marks them read with a POST `/messages/ack {ids}` after it printed them. Alternatively a short lease: `read_at` set, plus `delivered_at` confirmed on the next call.
- At-least-once is acceptable for chat; at-most-once with silent loss is not.

### 4. Low: an inbox long-poll ended by hub shutdown or client disconnect returns 500 and logs ERROR
- `internal/store/inbox_wait.go:82-83` returns `ctx.Err()`. `hub/respond.go` `fail()` maps it to the default branch: ERROR log "internal error err=context canceled" + 500.
- Reproduced: waiter on `sa`, `h.Stop()` → client gets `hub returned 500 internal: internal error`, and the hub logs ERROR.
- Every restart thus produces N ERROR lines and N 500s, and the background waiter (phase 2) sees a 500 instead of a clean empty result.
- Fix: in WaitInbox return `[]api.Message{}, nil` on ctx.Done, or in `fail()` treat `context.Canceled` as a quiet 503 `shutting_down`.

## Checked, not bugs
- `ReadTimeout: 30s` vs 50 s long-poll: empirically the handler context is NOT cancelled at ReadTimeout (Go standalone check: ReadTimeout 2 s, handler waited 5 s, 200 returned).
- DB/WAL/SHM perms: all `-rw-------` even in a 0755 dir (SQLite copies the main file's mode).
- Claim atomicity, fence bump on reclaim, stale requeue fence+1, cancel → `cancelled` on heartbeat/complete, idempotent Complete replay, single-statement inbox cap, mark-read atomicity across concurrent readers, token-hash uniqueness across tables, principal matrix (`isDaemon` excludes session tokens): all correct.
- Requeue keeping fence/claimed_by: a zombie gets `lease_lost` or a harmless `already_applied` (Fail/Release on `queued`); the next claim bumps the fence.

## Unresolved questions
1. Should a task that ends `stuck` at the attempt cap still accept the owner's late Complete (#1 fix, option b)? This is a product choice: is late work preferred over a clean stuck state?
2. Is at-least-once message delivery (possible duplicates in Claude context) acceptable as the trade for #3?
