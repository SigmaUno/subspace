export type TaskAction = "assign" | "reassign" | "cancel" | "reopen" | "set_budget" | "unblock";
export type AgentAction = "pause" | "resume" | "revoke" | "rotate-token";

export interface BulkResult {
  id: string;
  ok: boolean;
  error?: { code: string; message: string };
  connect?: { token: string };
}

export interface BulkResponse {
  action: string;
  results: BulkResult[];
  succeeded: number;
  failed: number;
}
