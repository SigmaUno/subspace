export type Repo = {
  name: string;
  host: string;
  base_url: string;
  default_branch: string;
  protected_branches: string[];
  merge_test_command: string[];
  merge_work_dir: string;
  deploy_command?: string[];
  token_env: string;
  agent_token_env: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
};

export type RepoList = { repos: Repo[] | null };

export type RepoHealth = {
  repo: string;
  ok: boolean;
  reachable: boolean;
  token_configured: boolean;
  token_hint: string;
  default_branch: string;
  default_branch_exists: boolean;
  host_default_branch: string;
  error: string;
  checked_at: string;
};
