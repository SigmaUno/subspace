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
  id: string;
  name: string;
  scope: "owner" | "viewer";
  created_at: string;
  last_used_at: string | null;
  revoked_at: string | null;
  expires_at: string | null;
}

export interface TokenList {
  tokens: OwnerToken[] | null;
}

export interface CreatedToken {
  token: OwnerToken;
  secret: string;
}

export interface Alert {
  id: string;
  severity: "warning" | "critical";
  title: string;
  summary: string;
  brief_id?: string;
  task_id?: string;
  agent_id?: string;
  status: "active" | "resolved";
  raised_at: string;
  resolved_at: string | null;
}

export interface AlertList {
  alerts: Alert[] | null;
}

export interface Breaker {
  provider: string;
  model: string;
  state: "closed" | "open" | "half_open";
  error_rate: number;
  samples: number;
  errors: number;
  threshold: number;
  opened_at: string | null;
  half_open_at: string | null;
}

export interface BreakerList {
  breakers: Breaker[] | null;
}

export interface WebhookOut {
  id: string;
  url: string;
  events: string[];
  brief_id: string | null;
  pending: number;
  delivered: number;
  dead: number;
  created_at: string;
}

export interface WebhookList {
  webhooks: WebhookOut[] | null;
}

export interface CreatedWebhook {
  webhook: WebhookOut;
  secret: string;
}

export interface WebhookDelivery {
  id: string;
  webhook_id: string;
  event: string;
  state: "pending" | "delivered" | "dead";
  attempts: number;
  next_attempt_at: string | null;
  last_status: number | null;
  last_error: string | null;
  created_at: string;
  updated_at: string;
  delivered_at: string | null;
}

export interface DeliveryList {
  deliveries: WebhookDelivery[] | null;
}

export interface RetentionSegment {
  id: number;
  kind: string;
  seq: number;
  file: string;
  rows: number;
  bytes: number;
  sha256: string;
  cutoff: string;
  first_id: number;
  last_id: number;
  status: "pending" | "archived" | "restored";
  created_at: string;
  updated_at: string;
}

export interface RetentionReport {
  at: string;
  resumed: number;
  archived: RetentionSegment[] | null;
  rows: Record<string, number> | null;
  wal_frames: number;
  checkpointed: boolean;
  vacuumed_pages: number;
  error?: string;
}

export interface RetentionStatus {
  policy: Record<string, string | number>;
  dir: string;
  anchor: { id: number; hash: string };
  segments: RetentionSegment[] | null;
  last_run: RetentionReport | null;
}

export interface RetentionVerify {
  ok: boolean;
  segments: number;
  rows: number;
  chain: { ok: boolean; events: number };
  broken_segment: number;
  error?: string;
}

export interface About {
  version?: string;
  uptime_seconds?: number;
  db_bytes?: number;
  backend?: string;
}
