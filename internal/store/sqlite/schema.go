package sqlite

import (
	"database/sql"
	"fmt"

	"github.com/SigmaUno/subspace/internal/audit"
	"github.com/SigmaUno/subspace/internal/model"
)

const schema = `
CREATE TABLE IF NOT EXISTS seq (name TEXT PRIMARY KEY, n INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT NOT NULL);

CREATE TABLE IF NOT EXISTS briefs (
  id TEXT PRIMARY KEY, title TEXT NOT NULL, body TEXT NOT NULL, repos TEXT NOT NULL, status TEXT NOT NULL,
  max_cost_eur REAL NOT NULL, max_parallel INTEGER NOT NULL, allowed_providers TEXT NOT NULL,
  plan_status TEXT NOT NULL, created_at INTEGER NOT NULL, cross_review INTEGER NOT NULL DEFAULT 1,
  auto_done_on_merge INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS plans (
  id TEXT PRIMARY KEY, brief_id TEXT NOT NULL, status TEXT NOT NULL, proposed_by TEXT NOT NULL,
  note TEXT, created_at INTEGER NOT NULL, decided_at INTEGER NOT NULL DEFAULT 0,
  warnings TEXT NOT NULL DEFAULT '[]'
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
  blocked_reason TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  priority INTEGER NOT NULL DEFAULT 0, component_id TEXT NOT NULL DEFAULT '',
  lease_expiries INTEGER NOT NULL DEFAULT 0
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
  task_id TEXT NOT NULL DEFAULT '', before TEXT NOT NULL DEFAULT '', after TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '',
  hash TEXT NOT NULL DEFAULT ''
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
` + auditNoUpdate + `
CREATE TRIGGER IF NOT EXISTS audit_no_delete BEFORE DELETE ON audit BEGIN SELECT RAISE(ABORT, 'audit is append-only'); END;
`

const auditNoUpdate = `CREATE TRIGGER IF NOT EXISTS audit_no_update BEFORE UPDATE ON audit BEGIN SELECT RAISE(ABORT, 'audit is append-only'); END;`

func migrate(db *sql.DB) error {
	for _, c := range []struct{ table, column, def string }{
		{"briefs", "cross_review", "INTEGER NOT NULL DEFAULT 1"},
		{"briefs", "auto_done_on_merge", "INTEGER NOT NULL DEFAULT 0"},
		{"tasks", "priority", "INTEGER NOT NULL DEFAULT 0"},
		{"tasks", "component_id", "TEXT NOT NULL DEFAULT ''"},
		{"plans", "warnings", "TEXT NOT NULL DEFAULT '[]'"},
		{"tasks", "lease_expiries", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := addColumn(db, c.table, c.column, c.def); err != nil {
			return err
		}
	}
	for _, c := range extraColumns {
		if err := addColumn(db, c.table, c.column, c.def); err != nil {
			return err
		}
	}
	return migrateAuditChain(db)
}

const auditHashColumn = `SELECT count(*) FROM pragma_table_info('audit') WHERE name='hash'`

func migrateAuditChain(db *sql.DB) error {
	var n int
	if err := db.QueryRow(auditHashColumn).Scan(&n); err != nil || n > 0 {
		return err
	}
	t, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = t.Rollback() }()
	if err := t.QueryRow(auditHashColumn).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if _, err := t.Exec(`ALTER TABLE audit ADD COLUMN hash TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := t.Exec(`DROP TRIGGER IF EXISTS audit_no_update`); err != nil {
		return err
	}
	links, err := chainExisting(t)
	if err != nil {
		return err
	}
	for _, l := range links {
		if _, err := t.Exec(`UPDATE audit SET hash = ? WHERE id = ?`, l.Hash, l.ID); err != nil {
			return err
		}
	}
	if _, err := t.Exec(auditNoUpdate); err != nil {
		return err
	}
	return t.Commit()
}

func chainExisting(t *sql.Tx) ([]model.AuditHead, error) {
	rows, err := t.Query(`SELECT id, ts, agent_id, role, tool, task_id, before, after, note FROM audit ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var (
		out  []model.AuditHead
		prev string
	)
	for rows.Next() {
		var r audit.Record
		if err := rows.Scan(&r.ID, &r.TS, &r.AgentID, &r.Role, &r.Tool, &r.TaskID, &r.Before, &r.After, &r.Note); err != nil {
			return nil, err
		}
		h, err := audit.Link(prev, r)
		if err != nil {
			return nil, fmt.Errorf("audit %d: %w", r.ID, err)
		}
		out = append(out, model.AuditHead{ID: r.ID, Hash: h})
		prev = h
	}
	return out, rows.Err()
}

func addColumn(db *sql.DB, table, column, def string) error {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('`+table+`') WHERE name=?`, column).Scan(&n)
	if err != nil || n > 0 {
		return err
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + def)
	return err
}

type columnDef struct{ table, column, def string }

var extraColumns []columnDef

func RegisterColumn(table, column, def string) {
	extraColumns = append(extraColumns, columnDef{table, column, def})
}
