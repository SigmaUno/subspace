export interface UsageTotals {
  requests: number;
  tokens_in: number;
  tokens_out: number;
  cache_read: number;
  cache_write: number;
  cost_usd: number;
  rate_limited: number;
}

export interface UsageGroup extends UsageTotals {
  key: string;
}

export interface UsageCredits {
  total_usd: number | null;
  spent_usd: number;
  remaining_usd: number | null;
}

export interface BriefBudget {
  brief_id: string;
  title: string;
  status: string;
  budget_eur: number;
  budget_usd: number;
  spent_usd: number;
  remaining_usd: number | null;
}

export interface UsageSummary {
  from: string;
  to: string;
  group_by: string;
  totals: UsageTotals;
  groups: UsageGroup[];
  credits: UsageCredits;
  briefs: BriefBudget[];
}

export interface UsagePoint extends UsageTotals {
  ts: string;
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
