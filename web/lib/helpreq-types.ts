export type HelpStatus = "open" | "accepted" | "superseded" | "closed";

export type ProposedTask = {
  ref?: string;
  title: string;
  spec: string;
  acceptance_criteria?: string[] | null;
  repo?: string;
  depends_on?: string[] | null;
  role?: string;
  budget?: { max_minutes?: number; max_cost_eur?: number } | null;
  priority?: number;
  component_id?: string;
};

export type HelpRequest = {
  id: string;
  type: "help" | "blocker" | "split";
  kind: string;
  task_id: string;
  brief_id: string;
  agent_id: string;
  role: string;
  needs: string;
  details: string;
  proposed_tasks: ProposedTask[] | null;
  status: HelpStatus;
  created_tasks: string[] | null;
  created_at: string;
};

export type HelpRequestList = { help_requests: HelpRequest[] | null };
