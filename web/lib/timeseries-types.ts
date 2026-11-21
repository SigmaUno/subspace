export type TimeseriesMetric = "tasks_done" | "tasks_created" | "cost_eur" | "active_agents" | "tokens" | "review_rounds";

interface TimeseriesPoint {
  t: string;
  v: number;
}

export interface Timeseries {
  metric: TimeseriesMetric;
  bucket: string;
  brief_id?: string;
  since: string;
  until: string;
  points: TimeseriesPoint[];
}
