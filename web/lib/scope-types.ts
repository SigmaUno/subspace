type ScopeFinding = {
  rule: string;
  severity: "error" | "warning";
  message: string;
  paths?: string[];
};

export type ScopeReport = {
  task_id: string;
  merge_eligible: boolean;
  checked_at: string | null;
  head_sha: string;
  files: number;
  additions: number;
  deletions: number;
  truncated: boolean;
  findings: ScopeFinding[] | null;
};
