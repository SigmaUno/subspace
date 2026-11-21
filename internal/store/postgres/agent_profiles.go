package postgres

func init() {
	RegisterSchema(`
CREATE TABLE IF NOT EXISTS agent_profiles (
  name TEXT COLLATE "C" PRIMARY KEY, role TEXT NOT NULL, kind TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL DEFAULT '',
  expires_in TEXT NOT NULL DEFAULT '', bulk_count INTEGER NOT NULL DEFAULT 0, callback_url TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '', created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL
);`, "agent_profiles")
}
