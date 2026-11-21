export const logLevels = ["debug", "info", "warn", "error"] as const;

export type LogLevel = typeof logLevels[number];

export interface LogLine {
  id: number;
  ts: string;
  level: LogLevel;
  msg: string;
  attrs: Record<string, string> | null;
}

export interface LogList {
  lines: LogLine[] | null;
}

export interface LogLevelInfo {
  level: LogLevel;
}
