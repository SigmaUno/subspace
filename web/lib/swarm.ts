import type { Message, TaskStatus } from "./types";

export interface Decision {
  id: string;
  kind: "plan" | "question" | "merge" | "unblock";
  title: string;
  body?: string;
  summary?: string;
  links?: { thread?: string; pr?: string };
  actions?: string[];
  brief_id?: string;
  task_id?: string;
  agent_id?: string;
  pr_url?: string;
  options?: string[];
  actions?: string[];
  impact?: number;
  created_at: string;
}

export interface Component {
  id: string;
  name: string;
  description?: string;
  depends_on?: string[];
  paths?: string[];
  owner_agent_ids?: string[];
  task_ids?: string[];
  status_counts?: Partial<Record<TaskStatus, number>>;
  progress_pct?: number;
  stale_count?: number;
  last_activity_at?: string;
}

export interface Conversation {
  id: string;
  messages: Message[];
}
