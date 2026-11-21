import type {
  UsageBriefBudget,
  UsageCredits,
  UsageGroup,
  UsagePoint,
  UsageTotals,
} from "./generated-types";

export type { UsageCredits, UsageGroup, UsagePoint, UsageTotals };
export type BriefBudget = UsageBriefBudget;

export interface UsageSummary {
  from: string;
  to: string;
  group_by: string;
  totals: UsageTotals;
  groups: UsageGroup[];
  credits: UsageCredits;
  briefs: BriefBudget[];
}

export interface UsageSeries {
  key: string;
  points: UsagePoint[];
}

export interface UsageSeriesResponse {
  from: string;
  to: string;
  bucket: string;
  group_by: string;
  series: UsageSeries[];
}

export interface ProviderStatus {
  provider: string;
  status: "ok" | "throttled" | "saturated";
  requests_1m: number;
  rate_limited_5m: number;
  rate_limited_1h: number;
  last_rate_limited_at: string | null;
  max_concurrency: number;
  in_use: number;
  concurrency_headroom: number;
  requests_per_min: number;
  rpm_headroom: number;
}

export interface ModelCapacity {
  provider: string;
  model: string;
  in_use: number;
  requests_1m: number;
  spawnable: number;
  limited_by: string;
}

export interface LiveCapacity {
  global_max_agents: number;
  agents_in_use: number;
  models: ModelCapacity[] | null;
  credits_remaining_usd: number | null;
}

export interface SpendWindow {
  window: "daily" | "weekly";
  cap_eur: number;
  spent_eur: number;
  percent: number;
  level: "off" | "ok" | "warning" | "exceeded";
  starts_at: string;
  resets_at: string;
  projected_exhaustion_at: string | null;
}

export interface SpendLimits {
  daily_cost_eur: number;
  weekly_cost_eur: number;
  org_budget_paused: boolean;
  windows: SpendWindow[];
}
