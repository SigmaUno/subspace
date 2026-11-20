export type DecisionKind = "question" | "plan" | "merge" | "unblock" | "stale_agent";

export type DecisionAction = "answer" | "approve" | "reject" | "merge" | "unblock" | "skip";

export interface Decision {
  id: string;
  kind: DecisionKind;
  title: string;
  summary: string;
  brief_id?: string;
  task_id?: string;
  agent_id?: string;
  links: { thread?: string; pr?: string };
  priority: number;
  created_at: string;
  actions: DecisionAction[];
}

export interface DecisionOutcome {
  id: string;
  action: DecisionAction;
  result: unknown;
  next: Decision | null;
}

export type DecisionPayload =
  | { action: "answer"; answer: string }
  | { action: "approve" | "merge"; note: string }
  | { action: "reject"; reason: string }
  | { action: "unblock"; note: string; status?: "ready" | "assigned" }
  | { action: "skip"; snooze: string };
