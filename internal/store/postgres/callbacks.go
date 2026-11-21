package postgres

func init() {
	RegisterSchema(`
ALTER TABLE agents ADD COLUMN IF NOT EXISTS callback_url TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS callback_secret TEXT NOT NULL DEFAULT '';`)
}
