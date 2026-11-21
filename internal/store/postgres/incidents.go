package postgres

func init() {
	RegisterSchema(`
CREATE TABLE IF NOT EXISTS incidents (
  id TEXT COLLATE "C" PRIMARY KEY, reason TEXT NOT NULL, actions TEXT NOT NULL DEFAULT '[]', results TEXT NOT NULL DEFAULT '[]',
  started_at BIGINT NOT NULL, ended_at BIGINT NOT NULL DEFAULT 0, actor TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS incidents_started ON incidents(started_at);`, "incidents")
}
