export type ActivitySeverity = "info" | "success" | "warning" | "error";

export interface ActivityRef {
  kind: "task" | "plan" | "brief" | "agent" | "question" | "spawn" | "help_request" | "artifact";
  id: string;
}

export interface ActivityItem {
  id: string;
  ts: string;
  source: "audit" | "message";
  tool?: string;
  actor: { kind: "agent" | "owner" | "system"; id: string };
  text: string;
  targets: ActivityRef[];
  severity: ActivitySeverity;
  icon: string;
  brief_id?: string;
  trace_id?: string;
}

export interface ActivityPage {
  items: ActivityItem[];
  next_cursor: string | null;
}
