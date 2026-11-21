package postgres

func init() {
	RegisterSchema(`ALTER TABLE briefs ADD COLUMN IF NOT EXISTS leader_id TEXT;`)
}
