# Daemon adversarial review (main 3095249)

Scope: internal/daemon. Covered: runner argv/env, deny floor, tool profiles, journal/replay, publish safety (gitleaks, sensitive paths, force-with-lease, supersede, PR template), orphan sweep, breaker, worktree GC, slots/throttle, lease_lost paths, config refusals. Threat model: the worker (`claude -p`, prompt from any session token) is untrusted. The daemon holds the GitHub credentials (gh.env / gh auth) and the host token.

Every fixed item has a regression test, and each test was confirmed to fail on the old code.

## Fixed

### 1. Critical: a worker can make the daemon's git run its own program with the GitHub credentials
- Where (old): every daemon git/gh/gitleaks call inside a task worktree (`publish_prepare.go` status/commit/rev-list/diff/log, `publish.go` rev-parse/push, `repo_clone.go` ls-remote, `github_pr.go` gh calls). These ran with `cwd=<worktree>` and relied on `<wt>/.git`, a plain file the worker can rewrite. `core.hooksPath=/dev/null` blocks hooks only, not other config-driven execution: `core.fsmonitor`, `remote.*.receivepack`, `gpg.program`, `credential.helper`, ...
- Exploit, reproduced in `TestWorkerCannotRedirectDaemonGitDir`:
  1. The worker creates `<wt>/.evil` (a git dir whose config sets `core.fsmonitor=<script>`) and writes `gitdir: .evil` into `<wt>/.git`.
  2. The daemon's publish `git status` runs the script with the daemon env (GH_TOKEN etc.).
  3. With remote/credential config, the same gitdir could also redirect the push.
- Fix (`repo_clone.go:35` `worktreeEnv`/`wtGit`):
  - every command inside a worktree runs with `GIT_DIR=<clone>/worktrees/<name>` (the daemon-owned admin dir, which must point back at this worktree) and `GIT_WORK_TREE=<wt>`; gh and gitleaks inherit the same env;
  - every daemon git call also passes `-c core.fsmonitor=false`.
  - Result: a tampered `.git` file is ignored, and the stray `.evil/` makes the task stuck ("untracked files left").

### 2. High: sensitive-path check bypasses (a PR could change agent config)
- Where (old): `publish_prepare.go` `sensitive` plus parsing of `git diff --name-only`.
  - a. git quotes non-ASCII paths, so `ünïcode/CLAUDE.md` arrived as `"\303…/CLAUDE.md"` with base `CLAUDE.md"`: missed.
  - b. A symlink entry named `.claude` (pointing at a normal dir with settings.json) is the path `.claude`, not `.claude/…`: missed.
  - c. Matching was case-sensitive, but macOS checkouts load `.Claude/` / `claude.md`.
  - d. `.github/actions/**` (run by workflows) wasn't covered.
  - e. `strings.Fields` split paths containing spaces.
- Reproduced: on the old code, tasks `sensitivequoted` and `symlinkclaude` completed with a PR.
- Fix:
  - `diff -z`, split on NUL;
  - `sensitive` (`publish_prepare.go:28`) is case-insensitive and segment-based: any `.claude` segment, root `.github` itself or `.github/workflows|actions/**`, `claude.md`/`claude.local.md`/`.gitleaks.toml`/`.gitleaksignore` anywhere, root `.mcp.json`.
- Tests: `TestPublishRefusalsLeaveTaskStuckWithWorktree` (2 new cases), `TestSensitivePaths` (more table rows).

### 3. High: deny floor let Edit/Write reach files that later run code as the user
- Where (old): `claude_args.go:16` `denyFloor` only covered `~/.ssh`, `~/.config`, `~/.claude*` and the state subdirs.
- An `edit-only` profile worker (no shell) could still edit:
  - `~/.gitconfig`, which is read by the daemon's own git: fsmonitor/credential helper, the same impact as #1;
  - shell rc files, `~/.local/bin/agent-mesh` (the binary that services run), and `~/Library/LaunchAgents`.
- It could also read `~/.git-credentials`, `~/.netrc` and `~/.aws`.
- Fix: these paths added to the Read+Edit floor. Test: `TestClaudeArgsIsolation`.

### 4. Medium (cheap): hub tokens in the worker env
- Where (old): `env.go:18` stripped only `AGENTMESH_TOKEN`. When the daemon was started from a shell, `AGENTMESH_SESSION_TOKEN`, `AGENTMESH_URL` and similar reached the worker.
- Fix: every `AGENTMESH_*` is stripped, then `AGENTMESH_HOOKS=off` is added. Test: `TestEnvFilesAndWorkerEnv`.

### 5. Medium (cheap): restart sweep deleted a worktree kept for a human
- Where (old): `orphans.go`. A crash after a task was reported stuck (worktree kept) but before its run record was removed (`task_exec.go:55`, `publish_complete.go`) left a lone run record. On restart the sweep released the task (the hub refuses: lease_lost) and then removed the worktree anyway.
- Fix:
  - the worktree is removed only when the release lands (`orphans.go:45`);
  - `finishPublish` now deletes the run record before the journal (`publish_complete.go:78`), so the crash window leaves only a journal, whose replay is idempotent.
- `release` now returns its error. Test: `TestRestartSweepKeepsWorktreeOfFinishedTask`.

## Not fixed (listed)

- **Medium, design: profiles that allow `Bash(go test *)` (the shipped default, `onboard/templates.go:68`) give the worker arbitrary code execution as the user.** Test code can read host.token/admin.token, use ssh-agent and git credential helpers, and ignores the deny floor, which only binds Claude's own tools. #1 and #3 matter for profiles without code execution (`edit-only`). Options:
  - document it next to the profile;
  - run workers as a separate OS user or inside a sandbox (bubblewrap/sandbox-exec);
  - keep the hub on another machine.
  The user should decide.
- **Medium, behaviour:** on a heartbeat `lease_lost` the daemon kills claude and deletes the worktree (`task_exec.go:58`). The hub now accepts a late Complete/Fail on a reaper-stuck task, but a running worker never gets to use it, because the reaper marks a task stuck only after TTL+30 min of silence, by which time the daemon has already killed it on its next heartbeat. Distinguishing "reaped stuck" from "reclaimed" needs a distinct hub code (hub is off-limits for this branch).
- **Low:** `findOrCreatePR` (`github_pr.go:74`) adopts any open PR whose `headRefName` equals the task branch, including one from a fork that uses the same name. Fix: also compare `headRepositoryOwner`.
- **Low:** `gc` derives the repo from the worktree's own `.git` file (`cloneOfWorktree`), which is worker-writable. That only picks which daemon clone runs `worktree remove`; the repo key is validated. Harmless, but it could use the run record or GIT_DIR naming instead.
- **Low:** `SSH_AUTH_SOCK` still reaches the worker; only relevant with code-execution profiles (see the first item).
- `daemon.go` is 201 lines after the `release` change.

## Checked, OK
- argv has no shell, the prompt goes via stdin, `--strict-mcp-config`, and settings sources are validated.
- bypassPermissions/auto are refused in config and in the per-claim `fits`, and bare `Bash` rules are refused.
- Env files must be 0600.
- The worktree base is validated with `ValidBaseBranch`, and `--end-of-options` is used everywhere.
- Journal: fsync + dir fsync, each step journaled before its side effect, replays idempotent (push compares the remote SHA; the PR is looked up by head before create).
- `--force-with-lease=<ref>:` refuses to overwrite.
- Supersede is limited to `agentmesh/task-<id>-f<k<fence>`.
- The PR body is templated, the model's final message is excluded, and the body is gitleaks-scanned.
- gitleaks runs with a dedicated exit code and `--ignore-gitleaks-allow`.
- Orphan kill checks the pid's cmdline.
- Claim retry reuses request_id.
- The breaker opens after 2 consecutive infra failures and preflight re-checks every 10 min.
- Throttle order: unhealthy → slots → cpu → mem → worktrees.

## Verification
- go vet ./... clean; go test -race -count=1 ./... all ok (daemon about 60 s); darwin/arm64 CGO_ENABLED=0 build ok.

## Unresolved questions
1. Accept "default profile = arbitrary code as the user" as documented, or isolate workers (separate user/sandbox)?
2. Should the hub return a distinct code for "reaped stuck at cap" so a daemon can finish its work instead of killing it?
