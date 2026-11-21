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

export interface ArchitectureNode {
  id: string;
  name: string;
  description?: string;
  depends_on?: string[];
  paths?: string[];
  owners?: { agent_id: string; task_ids?: string[] }[];
  status?: {
    counts?: Partial<Record<TaskStatus, number>>;
    progress_avg?: number;
    stale?: number;
    last_activity?: string | null;
  };
}

export function toComponent(node: ArchitectureNode): Component {
  return {
    id: node.id,
    name: node.name,
    description: node.description,
    depends_on: node.depends_on,
    paths: node.paths,
    owner_agent_ids: node.owners?.map((owner) => owner.agent_id),
    task_ids: node.owners?.flatMap((owner) => owner.task_ids ?? []),
    status_counts: node.status?.counts,
    progress_pct: node.status?.progress_avg,
    stale_count: node.status?.stale,
    last_activity_at: node.status?.last_activity ?? undefined,
  };
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
