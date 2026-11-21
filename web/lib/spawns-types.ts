export type SpawnLifecycle = "pending" | "connecting" | "active" | "idle" | "stopped" | "failed";

export type Spawn = {
  id: string;
  brief_id: string;
  agent_id: string;
  kind: string;
  model: string;
  provider: string;
  status: string;
  spawner: string;
  error: string | null;
  requested_by: string;
  created_at: string;
  lifecycle: SpawnLifecycle;
  connected_at: string | null;
  idle_since: string | null;
  stopped_at: string | null;
  stop_reason: string | null;
  process: "running" | "exited" | "unknown";
  process_checked_at: string | null;
};

export type SpawnList = { spawns: Spawn[] | null };
