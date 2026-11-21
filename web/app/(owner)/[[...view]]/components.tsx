"use client";

import { createContext, useContext, useEffect, useRef } from "react";

export const Clock = createContext(0);

export function Feedback({ error }: { error?: string }) {
  return error ? <p className="error" role="alert">{error}</p> : null;
}

export function LoadMore({ hasMore, loading, onMore }: { hasMore: boolean; loading: boolean; onMore: () => void }) {
  const sentinel = useRef<HTMLDivElement>(null);
  const latest = useRef(onMore);
  useEffect(() => { latest.current = onMore; });
  useEffect(() => {
    const node = sentinel.current;
    if (!node || !hasMore) return;
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) latest.current();
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, [hasMore, loading]);
  if (!hasMore) return null;
  return <div ref={sentinel} className="muted" role="status">{loading ? "Loading…" : "Scroll for more"}</div>;
}

export function staleAt(time: string | null, now: number, expires?: string | null) {
  return expires ? Date.parse(expires) <= now : !!time && now - Date.parse(time) > 15 * 60_000;
}

export function Heartbeat({ time, watch = true, expires }: { time: string | null; watch?: boolean; expires?: string | null }) {
  const date = time ? new Date(time) : null;
  const now = useContext(Clock);
  const stale = watch && staleAt(time, now, expires);
  return (
    <span className={stale ? "stale" : "meta"}>
      {date ? <time dateTime={time ?? undefined} title={time ?? undefined}>{date.toLocaleString([], { dateStyle: "short", timeStyle: "medium" })}</time> : "No heartbeat"}
      {stale && " · stale"}
    </span>
  );
}
