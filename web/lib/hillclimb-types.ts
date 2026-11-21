export interface HillclimbJob {
  id: string;
  brief_id: string;
  repo: string;
  metric_cmd: string;
  direction: "min" | "max";
  target: number | null;
  budget: number;
  plateau_n: number;
  status: string;
  stop_reason: string | null;
  best: number | null;
  best_attempt_id: number | null;
  attempts: number;
  since_best: number;
  created_at: string;
}

export interface HillclimbPoint {
  n: number;
  attempt_id: number;
  agent_id: string;
  branch: string;
  value: number | null;
  best: number | null;
  measured_at: string;
}

export interface HillclimbSeries {
  job: HillclimbJob;
  points: HillclimbPoint[];
}
