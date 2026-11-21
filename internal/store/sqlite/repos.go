package sqlite

func init() {
	RegisterSchema(`
CREATE TABLE IF NOT EXISTS repos (
  name TEXT PRIMARY KEY, host TEXT NOT NULL, base_url TEXT NOT NULL DEFAULT '', default_branch TEXT NOT NULL DEFAULT 'main',
  protected_branches TEXT NOT NULL DEFAULT '[]', merge_test_command TEXT NOT NULL DEFAULT '[]', merge_work_dir TEXT NOT NULL DEFAULT '',
  token_env TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  deploy_command TEXT NOT NULL DEFAULT '[]'
);`)
	RegisterExtensionColumn("repos", "agent_token_env", "TEXT NOT NULL DEFAULT ''")
	RegisterColumn("repos", "deploy_command", "TEXT NOT NULL DEFAULT '[]'")
}
