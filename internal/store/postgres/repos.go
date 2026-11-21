package postgres

func init() {
	RegisterSchema(`
CREATE TABLE IF NOT EXISTS repos (
  name TEXT COLLATE "C" PRIMARY KEY, host TEXT NOT NULL, base_url TEXT NOT NULL DEFAULT '', default_branch TEXT NOT NULL DEFAULT 'main',
  protected_branches TEXT NOT NULL DEFAULT '[]', merge_test_command TEXT NOT NULL DEFAULT '[]', merge_work_dir TEXT NOT NULL DEFAULT '',
  token_env TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL
);
ALTER TABLE repos ADD COLUMN IF NOT EXISTS agent_token_env TEXT NOT NULL DEFAULT '';
ALTER TABLE repos ADD COLUMN IF NOT EXISTS deploy_command TEXT NOT NULL DEFAULT '[]';`, "repos")
}
