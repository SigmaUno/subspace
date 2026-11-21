package sqlite

func init() {
	RegisterColumn("agents", "callback_url", "TEXT NOT NULL DEFAULT ''")
	RegisterColumn("agents", "callback_secret", "TEXT NOT NULL DEFAULT ''")
}
