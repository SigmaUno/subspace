export interface InsightEvidence {
  kind: "error" | "audit" | "message" | "task";
  id: string;
}

export interface InsightFinding {
  id: string;
  agent_id: string;
  rule: string;
  severity: "warning" | "high";
  summary: string;
  count: number;
  task_ids: string[];
  evidence: InsightEvidence[];
  since: string;
}

export interface Insights {
  generated_at: string;
  high: number;
  warning: number;
  findings: InsightFinding[];
}
