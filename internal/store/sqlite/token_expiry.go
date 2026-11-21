package sqlite

func init() {
	RegisterColumn("agents", "token_expires_at", "INTEGER NOT NULL DEFAULT 0")
}
