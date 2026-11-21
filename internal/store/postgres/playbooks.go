package postgres

func init() {
	RegisterSchema(`
CREATE TABLE IF NOT EXISTS playbooks (
  name TEXT COLLATE "C" PRIMARY KEY, description TEXT NOT NULL DEFAULT '', builtin INTEGER NOT NULL DEFAULT 0, rules TEXT NOT NULL,
  created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL
);`, "playbooks")
}
