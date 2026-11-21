import type { Message, Task } from "./types";

export interface TaskDetail extends Task {
  brief_id: string;
  plan_id: string | null;
  reviewer: string | null;
  review_rounds: number;
  tests: { passed: number; failed: number; reported_at: string | null } | null;
  created_at: string;
  updated_at: string;
}

export interface AuditEvent {
  id: number;
  ts: string;
  agent_id: string | null;
  role: string;
  tool: string;
  task_id: string | null;
  before: Record<string, unknown> | null;
  after: Record<string, unknown> | null;
  note: string | null;
}

export type TimelineEntry = {
  key: string;
  at: string;
  actor: string;
  title: string;
  detail?: string;
};

export type TaskMessages = { messages: Message[] | null };

export type AuditList = { events: AuditEvent[] | null };

export type TaskList = { tasks: TaskDetail[] | null };
