import type { CapacityLimit } from "./generated-types";

export const statuses = [
  ["backlog", "Backlog"], ["ready", "Ready"], ["assigned", "Assigned"],
  ["in_progress", "In progress"], ["submitted", "Submitted"], ["in_review", "In review"],
  ["changes_requested", "Changes requested"], ["approved", "Approved"], ["done", "Done"],
  ["blocked", "Blocked"], ["needs_owner", "Needs owner"], ["cancelled", "Cancelled"],
] as const;

export type TaskStatus = typeof statuses[number][0];

export interface Brief {
  id: string;
  title: string;
  body: string;
  repos: string[] | null;
  status: string;
  budget: { max_cost_eur: number; max_parallel_agents: number };
  allowed_providers: string[] | null;
  cost_used: number;
  plan_status: string;
  playbook?: string;
  hillclimb_jobs?: import("./swarm").HillclimbJob[];
}

export interface Task {
  id: string;
  title: string;
  spec: string;
  acceptance_criteria: string[] | null;
  repo: string;
  branch: string;
  role: string;
  assignee: string | null;
  component_id?: string | null;
  depends_on: string[] | null;
  status: TaskStatus;
  progress_pct: number;
  last_heartbeat_at: string | null;
  lease_expires_at: string | null;
  pr_url: string | null;
  budget: { max_minutes: number; max_cost_eur: number };
  cost_used: number;
  minutes_used: number;
  blocked_reason?: string | null;
}

export interface Agent {
  id: string;
  role: string;
  kind: string;
  model: string;
  provider: string;
  status: string;
  pause_reason: string | null;
  current_task_id: string | null;
  assigned_count: number;
  last_seen_at: string | null;
  last_message?: Message | null;
}

export interface Board {
  brief: Brief;
  columns: Partial<Record<TaskStatus, Task[] | null>>;
}

export interface Plan {
  id: string;
  brief_id: string;
  status: string;
  proposed_by: string;
  note: string | null;
  tasks: Task[] | null;
  warnings?: PlanFinding[] | null;
}

export interface PlanFinding {
  severity: string;
  rule: string;
  task?: string;
  message: string;
}

export interface Question {
  id: string;
  brief_id: string | null;
  task_id: string | null;
  agent_id: string;
  question: string;
  options: string[] | null;
  created_at: string;
}

export interface Thread {
  kind: "agent" | "task" | "brief";
  id: string;
  title?: string;
}

export interface Message {
  id: number;
  thread_kind?: Thread["kind"];
  thread_id?: string;
  author_id: string;
  author_role: string;
  kind: string;
  body: string;
  created_at: string;
}

export interface Connect {
  agent_id: string;
  join_line: string;
  mcp_url: string;
  snippets: { claude_code: string; codex_toml: string; opencode_json: string };
  token: string;
}

export interface Registration {
  agent: Agent;
  connect: Connect;
}

export interface Control {
  paused: boolean;
  pause_reason: string | null;
  counts: { open_questions: number; proposed_plans: number };
  version: string;
}

export type { CapacityLimit };

export interface Capacity {
  global_max_agents: number;
  limits: CapacityLimit[] | null;
  agents_in_use: number;
}
