export interface AgentTrendPoint {
  day: string;
  completed: number;
}

export interface AgentStats {
  agent_id: string;
  role: string;
  provider: string;
  status: string;
  tasks_completed: number;
  avg_duration_seconds: number;
  p90_duration_seconds: number;
  review_rounds_per_task: number;
  first_pass_approval_rate: number | null;
  blocked: number;
  expired: number;
  cost_per_task: number;
  tokens_per_task: number;
  trend: AgentTrendPoint[];
  score: number;
}
