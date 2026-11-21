export interface AgentQuota {
  max_cost_eur_per_day: number;
  max_tasks_per_day: number;
  max_concurrent_tasks: number;
}

export interface AgentQuotaUsage {
  cost_eur: number;
  tasks: number;
  concurrent_tasks: number;
  percent: number;
  level: "off" | "ok" | "warning" | "exceeded";
  day_starts_at: string;
  resets_at: string;
}

export interface AgentWithQuota {
  id: string;
  quota: AgentQuota;
  quota_usage: AgentQuotaUsage;
}
