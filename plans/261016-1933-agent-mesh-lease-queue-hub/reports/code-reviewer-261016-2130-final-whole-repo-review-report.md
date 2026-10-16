# Final whole-repo review (main bee064d)

Focus: cross-package seams no single owner reviewed. Covered:
- the hub↔cli session-worker flow;
- token kinds vs every REST route and MCP tool;
- schema v1→v6 on real old DB files;
- setup-claude ↔ onboard ↔ doctor (paths, URL resolution);
- release/install scripts;
- README claims vs behaviour.

No Critical found. Each fix has a regression test that fails on the old code.

## Fixed

### 1. High: tasks targeted at a session with worker mode off stay queued forever
- `store/claim_session.go` `AddTaskReplay`, `store/lease_maint.go` `reapTargetGone`.
- Problem:
  - a session started with `AGENTMESH_WORKER=off` can never claim (`sessionEligible` requires `s.worker = 1`);
  - yet `task_add target_session=<that session>` was accepted;
  - and the target-gone reaper treated a live non-worker as a valid target, so the task never ran and the creator was never told.
- Fix:
  - task add refuses a known non-worker target (`ErrNotWorker` → 409 `not_worker`, MCP text);
  - the reaper also sticks queued tasks whose target turned worker mode off, with reason "target session does not take tasks", which fires the creator notice.
- Test: `TestTasksForNonWorkerSessions`.

### 2. Medium: MCP `task_heartbeat` on a reaped task answered "internal error" and logged ERROR
- `mcptools/handler.go` `storeErr` had no case for `store.ErrLeaseReaped` (added with the late-finish change).
- The session was not told that it may still finish.
- Fix: a dedicated message telling it to finish and call task_done/task_fail.
- Test: `TestStoreErrExplainsLeaseReaped`.

### 3. Medium: the Stop-hook heartbeat stopped at the first refused task
- `cli/hook_session_worker.go` `heartbeatHeld` returned on the first error. One lapsed task (listed newest first) left the session's other held tasks un-extended, so they expired and were reclaimed.
- Fix: heartbeat every held task and join the errors.
- Test: `TestHeartbeatHeldExtendsEveryTaskDespiteOneRefusal`.
- cli edit is minimal (one function); tell #5.

### 4. Medium: doctor could pass while hooks used another hub
- doctor resolved the hub with `$AGENTMESH_URL` first, while hooks, the waiter and MCP (`SessionHubURL`) ignore it on purpose. With the variable set in a shell, doctor checked one hub and sessions talked to another.
- Fix:
  - `config.SessionHubURL(dir)` is the single resolver; `config.HubURL` = env, else `SessionHubURL`; `hubenv.SessionHubURL` delegates, removing a duplicate TOML reader;
  - doctor adds a Warn line "session hub" when the two differ.
- Test: `TestDoctorWarnsWhenEnvHubDiffersFromSessionHub`.

### 5. Test gaps closed
- **Real-file migrations.** `store/testdata/schema-v{1,3,5}.db` were written by the store code of f9e4209 (v1), 4360b42 (v3) and 1292771 (v5), checked out temporarily. Each has: one done, one stuck, one running and one queued task, two sessions, one unread message.
- `TestUpgradeRealOldDatabaseFiles` opens each file with today's code and checks:
  - integrity ok, user_version ≥ 6;
  - every old row and field kept, the old unread message still delivered;
  - the new features work on the upgraded file: idempotent add with replay, session claim with request id, done notice via trigger, Reap;
  - reopening is a no-op.
- All three pass, so no migration bug.
- The fixtures are force-added because `.gitignore` excludes `*.db`; they hold only placeholder data.
- The permission matrix test now includes `GET /api/v1/whoami` (any token).

## Checked, OK
- **REST matrix:**
  - admin only: cancel/requeue;
  - admin + session: task add (with replay), MCP, sessions list, send;
  - any token: reads, report, whoami;
  - host token only: claim, load, lease ops;
  - session token only: register/end, inbox, ack, session claim/heartbeat/complete/fail/release. The path session must belong to the token's host, and the holder `session:<id>` blocks touching another holder's task.
- **MCP:** per-principal tool sets (admin: cancel/requeue; session: inbox + worker tools); `adminOp` re-checks; `task_add from=` validated against the caller's host.
- **Session-worker flow end to end:**
  - SessionStart registers the repo and worker flag; prompt/stop keep the flag (nil);
  - ClaimableCount and claims honour worker = 1;
  - the Stop hook heartbeats, with a 30 min session TTL;
  - the done/stuck notice comes from the trigger, and the e2e suite covers it.
- **Tokens:**
  - hooks, waiter and MCP headers use only the session token, sent to the config-file hub;
  - the CLI uses `AGENTMESH_TOKEN` → session → admin;
  - report uses admin → session.
- **README and deployment guide:** every flag they mention exists in code (`--prompt-file`, `--log-file`, `--no-service`, etc.).
- **Install/release:** asset names match between `build-release.sh`, `release.yml` and `install.sh`. The checksum check and the https-only rule are unchanged since their review.

## Not fixed (listed)
- Low: `session-end` leaves tasks held by the ending session running until their 30 min lease lapses. Releasing them in the hook would requeue them at once. That's in cli, owned by #5.
- Low: CLI commands honour `$AGENTMESH_URL` with session.token as fallback. This is consistent with the documented design. A malicious project settings `env` could redirect it, but a project that can set env can also define hooks, so it adds no new capability.
- Note: `AGENTMESH_SESSION_TOKEN` env overrides session.token for hooks (documented).

## Verification
- gofmt clean, go vet ./... clean, go test -race -count=1 ./... all ok, darwin/arm64 CGO_ENABLED=0 build ok.

## Unresolved questions
1. Should the session-end hook release the tasks the session still holds (needs #5's agreement for cli)?
