export interface ReportStep {
  at: string;
  from: string;
  to: string;
  by: string;
  tool: string;
  elapsed_seconds: number;
}

export interface ReportReview {
  at: string;
  reviewer: string;
  verdict: string;
  wait_seconds: number;
}

export interface TaskReport {
  id: string;
  title: string;
  status: string;
  assignee: string;
  provider: string;
  cost_eur: number;
  created_at: string;
  claimed_at: string | null;
  submitted_at: string | null;
  done_at: string | null;
  claims: number;
  review_rounds: number;
  reviews: ReportReview[] | null;
  timeline: ReportStep[] | null;
  time_in_status: Record<string, number> | null;
  queue_seconds: number;
  work_seconds: number;
  review_seconds: number;
  blocked_seconds: number;
  active_seconds: number;
  lead_seconds: number;
}

export interface ReportCost {
  total_eur: number;
  by_task: Record<string, number> | null;
  by_agent: Record<string, number> | null;
  by_provider: Record<string, number> | null;
}

export interface ReportBucket {
  hour: string;
  done: number;
}

export interface ReportThroughput {
  done: number;
  window_hours: number;
  per_hour: number;
  hourly: ReportBucket[] | null;
}

export interface ReportCriticalPath {
  task_ids: string[] | null;
  seconds: number;
}

export interface ReportOutlier {
  task_id: string;
  metric: string;
  value: number;
  threshold: number;
}

export interface BriefReport {
  brief_id: string;
  title: string;
  generated_at: string;
  total: number;
  counts: Record<string, number> | null;
  tasks: TaskReport[] | null;
  cost: ReportCost;
  blocked_seconds: number;
  throughput: ReportThroughput;
  critical_path: ReportCriticalPath;
  outliers: ReportOutlier[] | null;
}
