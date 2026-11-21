export interface OutputLine {
  seq: number;
  agent_id: string;
  task_id: string | null;
  stream: "stdout" | "stderr" | "note";
  text: string;
  ts: string;
}

export interface AgentOutput {
  agent_id: string;
  lines: OutputLine[];
  first_seq: number;
  last_seq: number;
  next_since: number;
  more: boolean;
  gap: boolean;
}

export interface OutputChunk {
  agent_id: string;
  task_id: string | null;
  lines: OutputLine[];
}

export interface LatestOutput {
  agent_id: string;
  line: OutputLine;
  lines: number;
  updated_at: string;
}
