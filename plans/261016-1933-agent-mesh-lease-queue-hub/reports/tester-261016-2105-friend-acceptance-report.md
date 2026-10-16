# Friend acceptance test — install & use from README only

Date: 2026-10-16 · Base: main 3f3aba2 · Branch: test/acceptance
Setup: two fake machines = two temp HOME/XDG trees (A = hub "ubuntu", B = "laptop"), `env -i` minimal env, hub on 127.0.0.1:<free port>. Real `claude` binary on PATH but temp HOME (not logged in). Never touched real ~/.claude*, ~/.config/agent-mesh, systemd.

## Walkthrough (literal README order)

| Step | Result | Evidence |
|---|---|---|
| `make release` | PASS | 4 tar.gz + SHA256SUMS in dist/release |
| `scripts/install.sh` (AGENTMESH_DOWNLOAD_BASE=file://…, AGENTMESH_INSTALL_DIR=tmp) | PASS | checksum verified, 0755 binary, PATH warning + next steps printed; `agent-mesh version` = 3f3aba2 |
| A: `up --no-service --name ubuntu --listen 127.0.0.1:<port>` | PASS | config dir 0700; admin/host/session tokens + hub/daemon/client toml all 0600 |
| A: `agent-mesh hub` (background) | PASS | `/healthz` ok |
| A: `invite laptop` | PASS | loopback warning, `join <url>` + "paste this code", no restart needed |
| B: `join <url> --no-service`, code on stdin | PASS | tokens verified, client.toml hub_url set |
| B: `setup-claude --dry-run` on ClaudeKit-like settings.json (SessionStart/UserPromptSubmit/PreToolUse/SubagentStart hooks, statusLine, permissions, plugins, empty `"Stop": []`) | PASS | diff only ADDS one entry per event (+ SessionEnd); file sha unchanged |
| B: `setup-claude` ×2 | PASS | 2nd run "already up to date"; `claude mcp list` → agentmesh ✔ Connected (headersHelper) ; JSON valid |
| B: `setup-claude --uninstall` | PASS* | MCP removed; hooks removed. *Not byte-identical: originally empty `"Stop": []` dropped (semantically equal). Known; fix in progress in cli review fixes |
| A: same without empty arrays: setup → uninstall | PASS | `cmp` byte-identical |
| `doctor` A and B | PASS (expected fails) | hub reachable, token kinds host/session/admin ✓; ✗ only claude/gh login (temp HOME); service warnings |
| A: `task add --repo git@github.com:acme/lumio.git --budget 2 --host laptop` | PASS | stored canonical `github.com/acme/lumio`, target host laptop |
| `task list/get`, `report` on A and B | PASS | report shows Queued (1) |
| B: `hook session-start` in a lumio clone (https remote) | PASS | additionalContext with waiter + claim loop instructions |
| B: `inbox --wait --session <id>` | PASS | exits immediately: "1 task(s) can be claimed by this session" (targeted task, different remote form on A) |

## Plan acceptance criteria vs reality

| Criterion | Status | Evidence |
|---|---|---|
| 200-task/16-claimer exactly-once | Verified (tests) | store TestClaimExactlyOnceUnderContention; TestSessionClaimExactlyOnceAcross16Sessions |
| Task → healthy host → draft PR on `agentmesh/task-<id>-f<fence>`, templated body | Verified with fake gh/bare origin; **real GitHub PR unverified** | daemon integration tests, TestPRBodyHasNoModelText |
| Lease-lost never pushes; replayed complete keeps PR; crash after PR → no 2nd PR | Verified (tests) | TestLeaseLostMidRunKillsClaudeWithoutPublishing, TestCompleteIdempotent, TestCrashAfterPRCreatedReplaysWithoutSecondPR |
| Spec can't grant tools/bypass; worker isolation | Verified (tests + real `claude -p` smoke by daemon author) | TestClaudeArgsIsolation, TestTaskNeedingUnknownProfileIsNeverClaimed |
| Secret planted → publish refused | Verified (tests, real gitleaks) | TestPublishRefusalsLeaveTaskStuckWithWorktree |
| Hub restart mid-run no reclaim | Verified (tests) | TestRestartKeepsLeasesViaGrace, TestHubDownMidRunStillCompletes |
| Ubuntu reboot without login | **Unverified** (needs real reboot; user step) | — |
| Report content + host line | Verified (golden tests) | report golden fixture |
| Slot formula | Verified (tests) | TestDefaultSlots |
| Cross-host sessions/send/wake; hooks fail open | Verified at process level (this run + e2e + cli tests); **live Claude session wake: being verified separately** | TestWaiterWakesOnMessageWithinASecond, TestHooksFailOpenFastWhenHubDown, e2e two_hosts |
| setup-claude adds only / rerun no-op / uninstall byte-identical | Verified, except empty-array edge case | this run; TestMergeKeepsClaudeKitBytesAndUnmergeRestoresThem |
| No secrets/infra in repo; ~/.claude untouched without setup | Verified | gitleaks clean; this run used temp HOME |

## Friction found → fixed (docs/usage only)

1. Installer "Next steps" told users `join <hub-url> <bundle>` (old, leaks code to history) → now `join <hub-url>` then paste code (scripts/install.sh).
2. deployment-guide Mac fast path same stale form → fixed.
3. README: `task cancel|requeue` silently 403 off the hub machine → marked "hub machine only (admin token)" + troubleshooting entry.
4. README troubleshooting added: `hosts` shows "no hosts" with joined machines (only daemons report), missing `--budget` error text, doctor "service hub: not running" for manually started hub, moved binary / spaces in path breaks hooks.

## Code issues (reported to lead, not edited — owned by others)

- internal/cli: missing `--budget` → raw `max_budget_usd must be > 0 and <= 100`; suggest flag-named error or a default (e.g. 5).
- internal/cli: `task cancel` 403 message should say "needs admin token (hub machine)".
- internal/cli: `hosts` prints "no hosts" when no daemon ran; suggest listing joined hosts / sessions count, or hint text.
- internal/cli: no terminal `agent-mesh sessions` command (MCP only).
- internal/cli: `task list` hides target host; `task get` label `host:` ambiguous (target vs holder).
- internal/cli setup-claude: hook command is the unquoted absolute binary path → breaks if path contains spaces (e.g. macOS home with a space).
- internal/onboard doctor: "service hub: not running" even when hub reachable (manual run) — wording.

## Unverified (needs user / real environment)
- Real interactive Claude session wake via background waiter (separate live test in progress).
- Real GitHub push + draft PR; Mac launchd; Tailscale multi-host; reboot without login.

## Unresolved questions
- Should `task add` default `--budget` (friendlier) or stay required (explicit cost control)?
