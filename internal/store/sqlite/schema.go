package sqlite

import "database/sql"

const schema = `
CREATE TABLE IF NOT EXISTS seq (name TEXT PRIMARY KEY, n INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT NOT NULL);

CREATE TABLE IF NOT EXISTS briefs (
  id TEXT PRIMARY KEY, title TEXT NOT NULL, body TEXT NOT NULL, repos TEXT NOT NULL, status TEXT NOT NULL,
  max_cost_eur REAL NOT NULL, max_parallel INTEGER NOT NULL, allowed_providers TEXT NOT NULL,
  plan_status TEXT NOT NULL, created_at INTEGER NOT NULL, cross_review INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS plans (
  id TEXT PRIMARY KEY, brief_id TEXT NOT NULL, status TEXT NOT NULL, proposed_by TEXT NOT NULL,
  note TEXT, created_at INTEGER NOT NULL, decided_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS plans_brief ON plans(brief_id, status);

CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY, brief_id TEXT NOT NULL, plan_id TEXT NOT NULL, title TEXT NOT NULL, spec TEXT NOT NULL,
  acceptance TEXT NOT NULL, repo TEXT NOT NULL, branch TEXT NOT NULL, role TEXT NOT NULL,
  assignee TEXT NOT NULL DEFAULT '', reviewer TEXT NOT NULL DEFAULT '', depends_on TEXT NOT NULL,
  status TEXT NOT NULL, progress_pct INTEGER NOT NULL DEFAULT 0,
  last_heartbeat_at INTEGER NOT NULL DEFAULT 0, lease_expires_at INTEGER NOT NULL DEFAULT 0, claimed_at INTEGER NOT NULL DEFAULT 0,
  pr_url TEXT NOT NULL DEFAULT '', max_minutes INTEGER NOT NULL DEFAULT 0, max_cost_eur REAL NOT NULL DEFAULT 0,
  cost_used REAL NOT NULL DEFAULT 0, minutes_used INTEGER NOT NULL DEFAULT 0, review_rounds INTEGER NOT NULL DEFAULT 0,
  tests_passed INTEGER NOT NULL DEFAULT -1, tests_failed INTEGER NOT NULL DEFAULT 0, tests_at INTEGER NOT NULL DEFAULT 0,
  blocked_reason TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
DROP INDEX IF EXISTS tasks_brief;
CREATE INDEX IF NOT EXISTS tasks_brief_status ON tasks(brief_id, status, assignee);
CREATE INDEX IF NOT EXISTS tasks_assignee ON tasks(assignee, status);
CREATE INDEX IF NOT EXISTS tasks_reviewer ON tasks(reviewer, status);
CREATE INDEX IF NOT EXISTS tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS tasks_plan ON tasks(plan_id);

CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY, role TEXT NOT NULL, kind TEXT NOT NULL, model TEXT NOT NULL, provider TEXT NOT NULL,
  status TEXT NOT NULL, pause_reason TEXT NOT NULL DEFAULT '', token_hash TEXT NOT NULL,
  last_seen_at INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, spawned INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS agents_token ON agents(token_hash);

CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT, thread_kind TEXT NOT NULL, thread_id TEXT NOT NULL,
  author_id TEXT NOT NULL, author_role TEXT NOT NULL, kind TEXT NOT NULL, body TEXT NOT NULL, created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_thread ON messages(thread_kind, thread_id, id);

CREATE TABLE IF NOT EXISTS questions (
  id TEXT PRIMARY KEY, brief_id TEXT NOT NULL DEFAULT '', task_id TEXT NOT NULL DEFAULT '', agent_id TEXT NOT NULL,
  question TEXT NOT NULL, options TEXT NOT NULL, status TEXT NOT NULL, answer TEXT NOT NULL DEFAULT '',
  answered_at INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS questions_status ON questions(status);

CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, agent_id TEXT NOT NULL, role TEXT NOT NULL, tool TEXT NOT NULL,
  task_id TEXT NOT NULL DEFAULT '', before TEXT NOT NULL DEFAULT '', after TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS audit_agent ON audit(agent_id, id);
CREATE INDEX IF NOT EXISTS audit_task ON audit(task_id, id);

CREATE TABLE IF NOT EXISTS spawns (
  id TEXT PRIMARY KEY, brief_id TEXT NOT NULL, agent_id TEXT NOT NULL, kind TEXT NOT NULL, model TEXT NOT NULL,
  provider TEXT NOT NULL, status TEXT NOT NULL, spawner TEXT NOT NULL, error TEXT NOT NULL DEFAULT '',
  requested_by TEXT NOT NULL, created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS spawns_brief ON spawns(brief_id);

-- Append-only audit: refuse updates and deletes.
CREATE TRIGGER IF NOT EXISTS audit_no_update BEFORE UPDATE ON audit BEGIN SELECT RAISE(ABORT, 'audit is append-only'); END;
CREATE TRIGGER IF NOT EXISTS audit_no_delete BEFORE DELETE ON audit BEGIN SELECT RAISE(ABORT, 'audit is append-only'); END;
`

func migrate(db *sql.DB) error {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('briefs') WHERE name='cross_review'`).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		_, err = db.Exec(`ALTER TABLE briefs ADD COLUMN cross_review INTEGER NOT NULL DEFAULT 1`)
	}
	return err
}
