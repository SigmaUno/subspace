"use client";

import { createContext, useContext } from "react";

export const Clock = createContext(0);

export function Feedback({ error, loading }: { error?: string; loading?: boolean }) {
  if (error) return <p className="error" role="alert">{error}</p>;
  return loading ? <p className="muted" role="status">Loading…</p> : null;
}

export function Heartbeat({ time, watch = true, expires }: { time: string | null; watch?: boolean; expires?: string | null }) {
  const date = time ? new Date(time) : null;
  const now = useContext(Clock);
  const stale = watch && (expires ? Date.parse(expires) <= now : !!date && now - date.getTime() > 15 * 60_000);
  return (
    <span className={stale ? "stale" : "meta"}>
      {date ? <time dateTime={time ?? undefined} title={time ?? undefined}>{date.toLocaleString([], { dateStyle: "short", timeStyle: "medium" })}</time> : "No heartbeat"}
      {stale && " · stale"}
    </span>
  );
}
