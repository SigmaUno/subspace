export interface TranscriptQuery {
  since?: number;
  q?: string;
  at?: string;
}

export const transcriptPageSize = 500;

export function transcriptPath(taskID: string, query: TranscriptQuery = {}): string {
  const params = new URLSearchParams({ limit: String(transcriptPageSize) });
  if (query.since) params.set("since", String(query.since));
  if (query.q?.trim()) params.set("q", query.q.trim());
  if (query.at) params.set("at", query.at);
  return `tasks/${encodeURIComponent(taskID)}/transcript?${params}`;
}

export function localTimeToRFC3339(local: string): string | undefined {
  if (!local) return undefined;
  const time = new Date(local);
  if (Number.isNaN(time.getTime())) return undefined;
  return time.toISOString().replace(/\.\d{3}Z$/, "Z");
}

export function transcriptSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
}
