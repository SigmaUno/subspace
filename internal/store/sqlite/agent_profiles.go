package sqlite

func init() {
	RegisterSchema(`
CREATE TABLE IF NOT EXISTS agent_profiles (
  name TEXT PRIMARY KEY, role TEXT NOT NULL, kind TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '',
  expires_in TEXT NOT NULL DEFAULT '', bulk_count INTEGER NOT NULL DEFAULT 0, callback_url TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);`)
}
