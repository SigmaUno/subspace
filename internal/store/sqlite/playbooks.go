package sqlite

func init() {
	RegisterSchema(`
CREATE TABLE IF NOT EXISTS playbooks (
  name TEXT PRIMARY KEY, description TEXT NOT NULL DEFAULT '', builtin INTEGER NOT NULL DEFAULT 0, rules TEXT NOT NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);`)
}
