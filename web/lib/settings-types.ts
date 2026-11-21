export interface NotifyStatus {
  enabled: boolean;
  sinks: string[];
  sent: number;
  failed: number;
  dropped: number;
  queued: number;
}

export interface SinkResult {
  sink: string;
  ok: boolean;
  error?: string;
}

export interface Backup {
  name: string;
  path: string;
  bytes: number;
  verified: boolean;
  created_at: string;
  age_seconds: number;
}

export interface BackupList {
  backups: Backup[] | null;
}

export interface OwnerToken {
  id?: string;
  name?: string;
  created_at?: string | null;
  expires_at?: string | null;
  last_used_at?: string | null;
}

export interface TokenList {
  tokens: OwnerToken[] | null;
}

export type Alerts = Record<string, unknown>;

export interface About {
  version?: string;
  started_at?: string;
  uptime_seconds?: number;
  backend?: string;
  db_bytes?: number | null;
  wal_bytes?: number | null;
  backups?: { count: number; latest_at: string | null; latest_verified: boolean };
}
