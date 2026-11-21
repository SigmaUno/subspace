export interface AuditEntry {
  id: number;
  ts: string;
  agent_id: string | null;
  role: string | null;
  tool: string;
  task_id: string | null;
  before: unknown;
  after: unknown;
  note: string | null;
}

export interface AuditList {
  events: AuditEntry[] | null;
}

export interface AuditFilters {
  agent_id: string;
  task_id: string;
  tool: string;
  since: string;
}

export interface AuditVerify {
  ok?: boolean;
  valid?: boolean;
  status?: string;
  checked?: number;
  broken_at?: number | null;
  error?: string | null;
}
