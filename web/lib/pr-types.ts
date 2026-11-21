export type PRStatus = {
  task_id: string;
  brief_id: string;
  url: string;
  host: string;
  repo: string;
  number: number;
  state: "" | "open" | "merged" | "closed";
  draft: boolean;
  checks: "success" | "failure" | "pending" | "none";
  mergeable: boolean | null;
  head_sha: string;
  reviews: { user: string; state: string }[] | null;
  review_decision: "approved" | "changes_requested" | "none";
  merge_ready: boolean;
  error: string | null;
  checked_at: string | null;
  changed_at: string;
};

export type PRList = { prs: PRStatus[] | null };
