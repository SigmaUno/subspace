export interface IncidentFailure {
  agent_id: string;
  error: string;
}

export interface IncidentResult {
  action: string;
  status: "ok" | "failed";
  error?: string;
  detail?: string;
  agents?: string[];
  failures?: IncidentFailure[];
}

export interface Incident {
  id: string;
  reason: string;
  actions: string[];
  results: IncidentResult[];
  started_at: string;
  ended_at: string | null;
  actor: string;
}

export interface IncidentList {
  incidents: Incident[] | null;
}

export interface IncidentToken {
  agent_id: string;
  token?: string;
}

export interface IncidentStarted extends Incident {
  skipped?: string[];
  tokens?: IncidentToken[];
  succeeded: number;
  failed: number;
}

export interface IncidentEnded extends Incident {
  resumed_all: boolean;
  resumed_agents?: string[];
  failures?: IncidentFailure[];
}
