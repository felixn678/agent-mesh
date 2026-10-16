# Hub core + lease queue: implementation report

Status: DONE_WITH_CONCERNS (all success criteria pass; deviations below)

## Test results
- `go vet ./...` clean; `go test -race -count=3 ./...` pass (hub, store stable over 3 runs).
- `CGO_ENABLED=0` build OK for darwin/arm64 and linux/amd64.
- gitleaks (no-git scan) clean.
- Real SQLite temp files everywhere. Covered: 200 tasks / 16 claimers / 4 DB handles exactly-once; FIFO, host pin, caps filter, poison head not vetoing queue; fence/lease_lost/cancelled; expired reclaim; revival 10 min ok / 40 min lost; idempotent complete/fail/release (cost not double counted); reaper stuck-at-cap + stale requeue fence+1; grace (alive-at-crash only, once per downtime, fresh DB noop); AUTOINCREMENT; List/ListForReport; hosts upsert; HTTP: 401/403 permission matrix (admin/host/session x all endpoints), host from token (body host ignored), 413, validation 400s, claim->heartbeat->complete via client, hub restart on same port keeps leases; config/token CLI; sessions/messages/long-poll (wake <1 s, timeout empty, 20 waiters do not block claim, shutdown not blocked by waiters).

## Frozen contracts (exact exported signatures)
Module `github.com/felixn678/agent-mesh`, go 1.26.2.

Stubs for other sessions to fill (bodies only; signatures fixed):
- `internal/mcptools/handler.go`: `func NewHandler(st *store.Store) http.Handler` (go-sdk Stateless Streamable HTTP; no tools yet). Hub mounts at `/mcp` for admin + session tokens and puts the caller in ctx: `api.PrincipalFrom(r.Context())`.
- `internal/cli/run.go`: `func Run(ctx context.Context, args []string) error`. main passes the FULL args including subcommand for `task|hosts|inbox|hook|setup-claude`.
- `internal/daemon/run.go`: `func Run(ctx context.Context, args []string) error` (args exclude "daemon").
- `internal/report/build.go`: `type Input struct{ Now, Since time.Time; Tasks []api.Task; Hosts []api.Host }`, `func Build(in Input) string`; `internal/report/run.go`: `func Run(ctx context.Context, args []string) error`. Stub Build returns placeholder text; hub `GET /api/v1/report` calls it.

Store (`internal/store`):
- `Open(path string) (*sql.DB, error)`; `New(db *sql.DB, now func() time.Time, cfg LeaseConfig) *Store` (nil now = wall clock); `LeaseConfig{TTL, StaleRequeue, ReviveWindow, Retention, SessionAlive time.Duration}` (0 Retention=7d, 0 SessionAlive=12h); `DefaultLeaseConfig()`; `(*Store).Now() time.Time`, `Config() LeaseConfig`.
- Tasks: `Add(ctx, api.TaskSpec, createdBy string) (api.Task, error)` (defaults+validates), `Get(ctx,id)`, `List(ctx, state string, since time.Time) ([]api.Task, error)` (<=500, newest first), `ListForReport(ctx, since time.Time) ([]api.Task, error)`, `Cancel(ctx,id)`, `Requeue(ctx,id)`.
- Lease: `Claim(ctx, host string, api.ClaimRequest) (*api.ClaimedTask, error)` (nil,nil = none), `Heartbeat(ctx, host string, id, fence int64) error`, `Complete(ctx, host string, id int64, api.CompleteRequest) (alreadyApplied bool, err error)`, `Fail(... api.FailRequest)`, `Release(... api.ReleaseRequest)`.
- Maint: `Reap(ctx) (ReapResult{Stuck, Requeued, SessionsPruned, MessagesPruned int64}, error)`, `Grace(ctx) (int64, error)`.
- Hosts: `UpsertHostLoad(ctx, name string, api.HostLoad) error`, `ListHosts(ctx) ([]api.Host, error)`.
- Sessions: `RegisterSession(ctx, host string, api.RegisterSessionRequest) (api.Session, error)`, `EndSession(ctx, ownerHost string, api.EndSessionRequest) error`, `ListSessions(ctx, aliveOnly bool) ([]api.Session, error)`, `SendMessage(ctx, ownerHost string, api.SendMessageRequest) (api.Message, error)`, `Inbox(ctx, ownerHost, session string, markRead bool) ([]api.Message, error)`, `WaitInbox(ctx, ownerHost, session string, markRead bool, wait time.Duration) ([]api.Message, error)`. `ownerHost == ""` = unrestricted (admin).
- Errors: `ErrNotFound, ErrLeaseLost, ErrCancelled, ErrConflict, ErrSessionNotFound, ErrSessionOwner, ErrSessionEnded, ErrInboxFull`. Validation failures are `*api.ValidationError` (`api.IsValidation`).

API (`internal/api`): `TaskSpec{Repo, BaseBranch, Prompt, PermissionMode, ToolProfile, MaxBudgetUSD, Host, MaxAttempts}` (+`WithDefaults()`, `Validate()`), `Task`, `HostLoad`, `Host{Name; HostLoad; LastSeenAt}` + `Host.Summary()`, `ClaimRequest/ClaimedTask`, `HeartbeatRequest`, `CompleteRequest`, `FailRequest`, `ReleaseRequest`, `OutcomeResponse{AlreadyApplied}`, `ErrorBody`, `Session`, `RegisterSessionRequest`, `EndSessionRequest`, `SendMessageRequest`, `Message`, `Principal{Admin bool; Host string; Session bool}` + `CreatedBy()`, `WithPrincipal`, `PrincipalFrom`; validators `ValidRepo/ValidHostName/ValidBranch/ValidBaseBranch/ValidPermissionMode/ValidPRURL/ValidSessionID`; constants `State*`, `Code*` (lease_lost, cancelled, conflict, ended, inbox_full, too_many_waits, ...), `MaxMessageBytes=8192`, `SessionAliveWindow=12h`, `MaxUnreadPerSession=100`, `InboxLimit=50`, `MaxWaitsPerHost=50`.

Client (`internal/client`): `New(baseURL, token string) *Client`; admin/read: `AddTask, GetTask, ListTasks(ctx,state,since), CancelTask, RequeueTask, Hosts, Report(ctx, since time.Time) (string, error)`; daemon: `Claim(ctx, api.ClaimRequest) (api.ClaimedTask, error)` (`ErrNoTask` on 204), `ReportLoad(ctx, api.HostLoad) error`, `Heartbeat(ctx, id, api.HeartbeatRequest) error`, `Complete/Fail/Release(ctx, id, req) (alreadyApplied bool, err error)`; sessions: `RegisterSession(ctx, req) (api.Session, error)`, `EndSession(ctx, id string) error`, `ListSessions(ctx, aliveOnly bool)`, `SendMessage(ctx, req) (api.Message, error)`, `Inbox(ctx, session string, markRead bool)`, `InboxWait(ctx, session string, markRead bool, wait time.Duration)` (timeout = wait+10 s; wait capped 50 s). Errors: `*LeaseError{Code,Message}` (409 lease_lost/cancelled; `IsLeaseLost`), `*HTTPError{Status,Code,Message}` for everything else (e.g. 409 `ended`/`inbox_full`, 429 `too_many_waits`).

Hub / config / test harness:
- `hub.Config` (+`DefaultConfig`, `LoadConfig(path)`, `Validate`, `Lease()`), `hub.NewServer(cfg Config, st *store.Store) *Server`, `(*Server).Handler()`, `Serve(ctx, ln net.Listener) error`, `hub.ListenWithRetry`, `hub.Run(ctx, args)`.
- hub.toml adds `session_alive_window`, `retention`, `[session_tokens]` (host -> hash).
- `config`: `LoadTOML` (rejects unknown keys), `DefaultDir`, `ExpandHome`, `NewToken`, `HashToken`, `ParseHash`, `WriteToken` (0600, no overwrite), `ReadToken` (rejects group/world-readable).
- `hubtest.Start(t testing.TB, hosts ...string) *Hub` (default host-a, host-b; each gets a host token AND a session token): fields `URL, Store, DB, Clock, AdminToken, Config`; `HostToken(name)`, `SessionToken(name)`, `Admin()`, `Host(name)`, `Session(name)` clients; `Restart()` (same port, reopens DB, grace runs), `Stop()`. `hubtest.Clock{Now, Advance}`. Reaper interval is 1 h in tests: call `h.Store.Reap` explicitly.
- `version.Version` (ldflags).
- CLI: `agent-mesh token new (--admin | --host <n> | --session <n>) --out <file>`; main dispatches `hub|daemon|task|hosts|inbox|hook|setup-claude|report|token|version`.

REST (final): see phase-01 table. Permission model: admin=all; host (daemon) token = claim/load/heartbeat/complete/fail/release + reads (tasks, hosts, report); session token = sessions register/end/list, messages send, inbox (owner only), task add (`created_by=host:<name>`), MCP, same reads; cancel/requeue admin only. Admin may also list sessions and send messages (any from), but gets 403 on register/end/inbox. Routes: `POST /sessions`, `POST /sessions/{id}/end` (204), `GET /sessions?alive=1`, `POST /messages`, `GET /messages/inbox?session=&mark_read=1&wait=<N|Ns>`.

## Decision to note: session token
A separate per-host session token exists (open question 9 resolution): hub.toml `[session_tokens]`, `token new --session`. It cannot do lease/load ops or cancel/requeue. Token hashes must be unique across admin/host/session tables (config validation). Daemon (host) tokens are NOT accepted for sessions/messages/task add; hooks and `setup-claude` must use the session token file.

## Deviations from plan
- Session endpoints follow the plan REST table (`POST /sessions`, `/sessions/{id}/end`), not the earlier coordinator note (`/sessions/register`, `/sessions/end`).
- No server `WriteTimeout` (per plan); long-poll uses request-context cancel (server `BaseContext` ends on shutdown). Per-host cap 50 concurrent waits -> 429.
- Safety re-poll of waiting inbox every 2 s in addition to in-memory notify.
- `ListForReport` also includes `stuck` tasks (plan said non-terminal = queued+running); stuck is actionable. Cap 1000.
- `Principal` has an extra `Session bool` field beyond plan `{Admin, Host}`.
- `ValidBranch` (generic, allows `agentmesh/`) plus `ValidBaseBranch` (rejects it).
- `main` routes `inbox` to `cli.Run` too (plan listed only hook|setup-claude); no separate hook/setup functions: they go through `cli.Run(ctx, args)` with args[0] = subcommand.
- Prompt limit 64 KB vs request limit 128 KB: a prompt with heavy JSON escaping can exceed 128 KB and get 413; accepted.
- Reaper GC runs inside `Reap`; first reap happens one interval after start (crash-loop grace safety).
- `hubtest.Start` takes `testing.TB` and variadic host names.
- Files over 200 LOC: client.go 298, sessions_test.go 296, validate.go 225, types.go 205, sessions.go 210 (cohesive; not split).
- Docs (`docs/`) not touched (phase 5 owns them).

## Unresolved questions
1. Should admin be able to register/end/read inbox for sessions (needed if the admin-token `.mcp.json` session in this repo should use `inbox`/`sessions` tools as itself)? Currently 403 per plan; phase 2 must decide (e.g. use a session token there).
2. Cancelled-then-requeued task: old worker with the same fence gets `lease_lost` (not `cancelled`) after requeue; believed correct.
3. `retention` also bounds unseen sessions (7 d) while alive window is 12 h; confirm intended.

Status: DONE_WITH_CONCERNS

## Interactive session workers (additive contract)
Schema v2 (auto-upgrade from v1 on open). Existing daemon path unchanged except: daemon claim skips tasks with `target_session` and honours `target_host`.

- Repo identity: `api.NormalizeRepo(remote) (string, error)` -> `host/owner/name` lowercase (strips scheme, user, port, `.git`, trailing `/`; scp-like `git@host:o/n` ok; >= 3 segments, host has a dot). `api.CanonicalRepo(s)`: anything containing `/` or `:` is normalized, plain logical keys pass through. Task repos are stored canonical; daemon claim `repos` entries are canonicalized too; session `repo` is normalized at register (empty body value keeps the stored one).
- API fields: `TaskSpec/Task` += `TargetHost`, `TargetSession`, `CreatedBySession`; `Task` += `Summary`; `CompleteRequest` += `Summary` (<= 2000 B); `FailRequest` += `SessionID` (store keeps old value when empty); `RegisterSessionRequest/Session` += `Repo`; `api.InboxResponse{Messages []Message; ClaimableCount int}`.
- `created_by_session` is verified: session token -> session must exist on the token's host (403/404 otherwise); admin -> must exist. Use `Store.AddTask` (not `Add`) from MCP/REST so this check runs.
- Holder for session leases: `"session:<id>"`; lease TTL `session_lease_ttl` (hub.toml, default 30 min, `store.DefaultSessionTTL`), extended by heartbeat. Same fence/idempotency semantics as daemon leases.
- REST (session token only; path session must belong to the token's host, else 403): `POST /api/v1/sessions/{id}/claim` (200 ClaimedTask | 204), `POST /api/v1/sessions/{id}/tasks/{tid}/{heartbeat|complete|fail|release}` (bodies as daemon endpoints). A session acting on a task held by another holder gets 409 `lease_lost`. Claim errors: 404 unknown session, 403 other host, 409 `ended` (ended or outside alive window).
- Claim rule: session alive and `repo <> ''`; task `repo == session.repo`, state queued (or lease expired), attempts left, `host`/`target_host` null or this host, `target_session` null or this session; oldest id first.
- Completion notice: SQLite trigger inserts a message (from_session/from_host `hub`) to `created_by_session` when state becomes `done` or `stuck` (complete, fail-without-retry, reaper at cap). Body: `[agent-mesh] task #N <state> (repo R)` + `branch:`/`pr:`/`result:` lines (result = summary for done, reason for stuck, <= 2000 B). Bypasses the unread cap. Cancel and retry-requeue send nothing.
- Waiter: `GET /messages/inbox?...&claimable=1` returns `api.InboxResponse` (object, not array) and also wakes (without claiming) when `ClaimableCount > 0`. Task add, requeue, release/fail-retry and reaper broadcast to all waiters; 2 s safety re-poll remains.
- Client (`session` token): `SessionClaim(ctx, session) (api.ClaimedTask, error)` (`ErrNoTask`), `SessionHeartbeat(ctx, session, id, api.HeartbeatRequest) error`, `SessionComplete/SessionFail/SessionRelease(ctx, session, id, req) (alreadyApplied bool, err error)`, `InboxWaitClaimable(ctx, session, markRead, wait) (api.InboxResponse, error)`.
- Store: `ClaimForSession(ctx, host, id) (*api.ClaimedTask, error)`, `ClaimableCount(ctx, id) (int, error)`, `SessionHolder(ctx, ownerHost, id) (string, error)`, `AddTask(ctx, spec, api.Principal) (api.Task, error)`, `WaitInboxClaimable(...) (api.InboxResponse, error)`; `LeaseConfig.SessionTTL`; consts `SessionHolderPrefix`, `DefaultSessionTTL`. Existing Heartbeat/Complete/Fail/Release take the holder string where they took the host.
- Permission matrix: session token gains the claim/tasks routes above; daemon and admin tokens get 403 on them.
