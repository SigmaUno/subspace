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
