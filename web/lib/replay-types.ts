export interface ReplayTask {
  id: string;
  title: string;
  role: string;
  status: string;
  assignee: string | null;
  reviewer: string | null;
  progress_pct: number;
  cost_used: number;
  changed_audit_id: number;
  changed_at: string | null;
}

export interface ReplayAgent {
  id: string;
  role: string;
  assigned: string[] | null;
  reviewing: string[] | null;
  events: number;
  last_event_at: string | null;
}

export interface ReplaySnapshot {
  brief_id: string;
  audit_id: number;
  at: string | null;
  events: number;
  total: number;
  counts: Record<string, number> | null;
  columns: Record<string, ReplayTask[] | null>;
  agents: ReplayAgent[] | null;
}

export interface ReplayPoint {
  audit_id: number;
  at: string;
  counts: Record<string, number> | null;
}

export interface ReplayKeyEvent {
  audit_id: number;
  at: string;
  tool: string;
  agent_id: string;
  task_id: string;
  from: string;
  to: string;
  transcript?: string;
}

export interface ReplayTimeline {
  brief_id: string;
  events: number;
  start: string | null;
  end: string | null;
  points: ReplayPoint[] | null;
  key_events: ReplayKeyEvent[] | null;
}
