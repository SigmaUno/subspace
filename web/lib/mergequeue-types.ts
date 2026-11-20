export type CandidateState = "queued" | "running" | "waiting" | "merged" | "ejected";

export interface Candidate {
  id: string;
  repo: string;
  pr: number;
  head: string;
  base: string;
  task_id: string;
  state: CandidateState;
  merged_sha?: string;
  log?: string;
  enqueued_at: string;
  updated_at: string;
}

export type CandidateList = { candidates: Candidate[] | null };

export type EnqueueRequest = {
  repo: string;
  pr: number;
  head: string;
  base: string;
  task_id: string;
};
