package sqlite

func init() {
	RegisterColumn("briefs", "playbook", "TEXT NOT NULL DEFAULT ''")
}
