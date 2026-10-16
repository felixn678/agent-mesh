-- Schema version 5 (older databases gain later columns via ALTER and the v4 trigger, see db.go upgrades). Statements are idempotent so concurrent first opens are safe.
CREATE TABLE IF NOT EXISTS tasks (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,  -- ids never reused (branch names embed id)
  repo             TEXT NOT NULL,                 -- logical key; host maps to clone URL
  base_branch      TEXT NOT NULL DEFAULT 'main',
  prompt           TEXT NOT NULL,
  permission_mode  TEXT NOT NULL DEFAULT 'acceptEdits' CHECK (permission_mode IN ('acceptEdits','dontAsk','plan')),
  tool_profile     TEXT NOT NULL DEFAULT 'default', -- name only; host resolves to tools
  max_budget_usd   REAL NOT NULL,
  host             TEXT,                          -- optional constraint
  state            TEXT NOT NULL DEFAULT 'queued'
                   CHECK (state IN ('queued','running','done','stuck','cancelled')),
  claimed_by       TEXT,
  fence            INTEGER NOT NULL DEFAULT 0,
  attempts         INTEGER NOT NULL DEFAULT 0,
  max_attempts     INTEGER NOT NULL DEFAULT 3,
  lease_expires_at INTEGER,                       -- kept on reaper-stuck rows: the owner may still finish
  last_heartbeat_at INTEGER,
  branch           TEXT,
  pr_url           TEXT,
  session_id       TEXT,
  worktree         TEXT,
  cost_usd         REAL NOT NULL DEFAULT 0,       -- summed over attempts
  publish_note     TEXT,                          -- e.g. leftover_commit, no_changes, pr_failed
  reason           TEXT,                          -- last failure/stuck/release reason (redacted)
  created_by       TEXT,
  target_host      TEXT,                          -- v2: only this host may claim
  target_session   TEXT,                          -- v2: only this interactive session may claim
  created_by_session TEXT,                        -- v2: gets a hub message when the task ends
  summary          TEXT,                          -- v2: worker result text
  claim_request_id TEXT,                          -- v3: holder's request id of the current claim (retry replay)
  request_id       TEXT,                          -- v5: creator's request id of the add (retry replay)
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  finished_at      INTEGER
);
CREATE INDEX IF NOT EXISTS tasks_claim ON tasks(state, id);
CREATE INDEX IF NOT EXISTS tasks_updated ON tasks(updated_at);
-- v5: idempotent task add; one request id per creating principal.
CREATE UNIQUE INDEX IF NOT EXISTS tasks_request_id ON tasks(created_by, request_id) WHERE request_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS hosts (
  name             TEXT PRIMARY KEY,
  cores            INTEGER NOT NULL DEFAULT 0,
  ram_gb           INTEGER NOT NULL DEFAULT 0,
  cpu_model        TEXT,
  slots            INTEGER NOT NULL DEFAULT 0,
  slots_overridden INTEGER NOT NULL DEFAULT 0,
  cpu_pct          REAL NOT NULL DEFAULT 0,
  mem_avail_pct    REAL NOT NULL DEFAULT 0,
  agent_worktrees  INTEGER NOT NULL DEFAULT 0,
  running          INTEGER NOT NULL DEFAULT 0,
  throttle_reason  TEXT,
  claude_version   TEXT,
  agent_version    TEXT,
  last_seen_at     INTEGER NOT NULL
);

-- last_alive_at: last reaper tick; last_grace_at: last startup grace.
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL);

-- Claude Code sessions registered by hooks; alive = recent last_seen_at and not ended.
CREATE TABLE IF NOT EXISTS sessions (
  id           TEXT PRIMARY KEY,                  -- Claude session_id
  host         TEXT NOT NULL,                     -- from the token, never the body
  cwd          TEXT NOT NULL DEFAULT '',
  name         TEXT NOT NULL DEFAULT '',
  repo         TEXT NOT NULL DEFAULT '',          -- v2: normalized project key
  started_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  ended_at     INTEGER
);
CREATE INDEX IF NOT EXISTS sessions_seen ON sessions(last_seen_at);

-- Opaque session-to-session messages; the hub never interprets body.
CREATE TABLE IF NOT EXISTS messages (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  from_session TEXT NOT NULL,
  from_host    TEXT NOT NULL,
  to_session   TEXT NOT NULL,
  body         TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  read_at      INTEGER
);
CREATE INDEX IF NOT EXISTS messages_inbox ON messages(to_session, read_at, id);

-- Completion notice: when a task created by a session reaches done or stuck,
-- the creator gets a message from "hub" in the same transaction as the state
-- change, so no code path (complete, fail, reaper) can skip it. v4: the owner's
-- late Fail of a reaper-stuck task (stuck -> stuck, lease marker cleared) sends
-- a second notice carrying the worker's real reason.
CREATE TRIGGER IF NOT EXISTS tasks_notify_creator AFTER UPDATE OF state, lease_expires_at ON tasks
WHEN NEW.created_by_session IS NOT NULL AND NEW.state IN ('done','stuck') AND (OLD.state <> NEW.state
  OR (OLD.state = 'stuck' AND OLD.lease_expires_at IS NOT NULL AND NEW.lease_expires_at IS NULL))
BEGIN
  INSERT INTO messages (from_session, from_host, to_session, body, created_at)
  VALUES ('hub', 'hub', NEW.created_by_session,
    '[agent-mesh] task #' || NEW.id || ' ' || NEW.state || ' (repo ' || NEW.repo || ')'
      || COALESCE(char(10) || 'branch: ' || NEW.branch, '')
      || COALESCE(char(10) || 'pr: ' || NEW.pr_url, '')
      || COALESCE(char(10) || 'result: ' || substr(CASE WHEN NEW.state = 'done' THEN NEW.summary ELSE NEW.reason END, 1, 2000), ''),
    NEW.updated_at);
END;
