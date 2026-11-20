export interface AuditEvent {
  id: number;
  ts: string;
  agent_id: string | null;
  role: string | null;
  tool: string;
  task_id: string | null;
  before: Record<string, unknown> | null;
  after: Record<string, unknown> | null;
  note: string | null;
}

export interface AuditList {
  events: AuditEvent[] | null;
}
