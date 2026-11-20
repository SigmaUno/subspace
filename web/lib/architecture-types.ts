export interface ArchitectureOwner {
  agent_id: string;
  role: string;
  agent_status: string;
  task_ids: string[] | null;
}

export interface ArchitectureRollup {
  counts: Record<string, number> | null;
  total: number;
  progress_avg: number;
  blocked: number;
  stale: number;
  last_activity: string | null;
}

export interface ArchitectureNode {
  id: string;
  brief_id: string;
  name: string;
  description: string;
  paths: string[] | null;
  depends_on: string[] | null;
  owners: ArchitectureOwner[] | null;
  status: ArchitectureRollup;
}

export interface ArchitectureEdge {
  from: string;
  to: string;
}

export interface ArchitectureGraph {
  brief_id: string;
  nodes: ArchitectureNode[] | null;
  edges: ArchitectureEdge[] | null;
}

export interface ComponentSpec {
  name: string;
  description: string;
  paths: string[];
  depends_on: string[];
}

export type RollupState = "empty" | "pending" | "active" | "stale" | "blocked" | "done";
