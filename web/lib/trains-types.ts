export type TrainState = "building" | "testing" | "merging" | "merged" | "failed" | "aborted";

export type MemberResult = "pending" | "merging" | "merged" | "ejected" | "requeued";

export type TrainMember = {
  candidate_id: string;
  task_id: string;
  pr: number;
  head: string;
  sha: string;
  result: MemberResult;
};

export type Train = {
  id: string;
  repo: string;
  base: string;
  state: TrainState;
  base_sha: string;
  tip: string;
  members: TrainMember[] | null;
  runs: number;
  log?: string;
  created_at: string;
  updated_at: string;
};

export type TrainList = { trains: Train[] | null };
