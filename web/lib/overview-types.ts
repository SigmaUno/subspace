import type { Brief, Control, TaskStatus } from "./types";

export type SwarmState = Omit<Control, "counts"> & {
  counts: Control["counts"] & {
    agents: number;
    agents_active: number;
    tasks: Partial<Record<TaskStatus, number>> | null;
  };
};

export interface AuditEvent {
  id: number;
  ts: string;
  agent_id: string;
  role: string;
  tool: string;
  task_id: string | null;
  before: { status?: string } | null;
  after: { status?: string } | null;
  note: string | null;
}

export interface BriefProgress {
  brief: Brief;
  counts: Partial<Record<TaskStatus, number>>;
  total: number;
}

export interface SwarmBoards {
  briefs: BriefProgress[];
  titles: Record<string, string>;
}

export interface DecisionInbox {
  decisions: { id: string }[] | null;
}
