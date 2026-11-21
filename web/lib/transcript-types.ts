export interface TranscriptLine {
  offset: number;
  ts: string;
  agent_id: string;
  stream: "stdout" | "stderr" | "note";
  text: string;
}

export interface TranscriptPage {
  task_id: string;
  lines: TranscriptLine[] | null;
  first_offset: number;
  last_offset: number;
  next_since: number;
  more: boolean;
  gap: boolean;
  segments: number;
  bytes: number;
}
