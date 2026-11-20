import type { Message, TaskStatus } from "./types";

export interface Decision {
  id: string;
  kind: "plan" | "question" | "merge" | "unblock";
  title: string;
  body?: string;
  brief_id?: string;
  task_id?: string;
  agent_id?: string;
  pr_url?: string;
  options?: string[];
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

export interface HillclimbJob {
  id: string;
  title?: string;
  metric: string;
  direction: "min" | "max";
  status: string;
  stop_reason?: string;
  target?: number;
}

export interface Attempt {
  id: string;
  value: number | null;
  branch?: string;
  branch_url?: string;
  created_at: string;
}

export interface Series {
  job?: HillclimbJob;
  attempts: Attempt[];
}
