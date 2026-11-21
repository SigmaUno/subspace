"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api, useResource } from "../../../lib/client";
import type { ActivityItem, ActivityPage, ActivityRef, ActivitySeverity } from "../../../lib/activity-types";
import type { Brief, Thread } from "../../../lib/types";
import { Feedback } from "./components";
import "./activity-feed.css";

const pageSize = 50;
const severities: ActivitySeverity[] = ["info", "success", "warning", "error"];
const glyphs: Record<string, string> = {
  activity: "•", split: "⑂", plan: "☰", question: "?", check: "✓", user: "☺", alert: "!", bot: "⚙", x: "✕",
  play: "▶", folder: "▣", plus: "+", file: "▤", settings: "⚙", edit: "✎", stop: "■", pause: "❚❚",
  comment: "✉", spawn: "⇡", lock: "🔒", budget: "€", send: "➤",
};

type Tagged = ActivityPage & { path: string };
type Accumulated = { key: string; items: ActivityItem[]; cursor: string | null; merged?: Tagged };

const loadPage = (path: string, signal: AbortSignal) =>
  api<ActivityPage>(path, "GET", undefined, signal).then((page): Tagged => ({ ...page, items: page.items ?? [], path }));

function union(current: ActivityItem[], incoming: ActivityItem[]) {
  const known = new Set(current.map((item) => item.id));
  return [...current, ...incoming.filter((item) => !known.has(item.id))]
    .sort((a, b) => b.ts.localeCompare(a.ts) || b.id.localeCompare(a.id));
}

function threadOf(ref: ActivityRef): Thread | null {
  return ref.kind === "task" || ref.kind === "brief" || ref.kind === "agent" ? { kind: ref.kind, id: ref.id } : null;
}

const stamp = (time: string) => new Date(time).toLocaleString([], { dateStyle: "short", timeStyle: "medium" });

export function ActivityFeed({ briefID, revision, openThread }: {
  briefID?: string; revision: number; openThread: (thread: Thread) => void;
}) {
  const [brief, setBrief] = useState(briefID ?? "");
  const [severity, setSeverity] = useState("");
  const query = new URLSearchParams({ limit: String(pageSize) });
  if (brief) query.set("brief_id", brief);
  if (severity) query.set("severity", severity);
  const path = `activity?${query}`;
  const briefs = useResource<{ briefs: Brief[] }>("briefs?limit=1000", 0);
  const head = useResource<Tagged>(path, revision, loadPage);
  const [acc, setAcc] = useState<Accumulated>({ key: path, items: [], cursor: null });
  const [loading, setLoading] = useState(false);
  const [moreError, setMoreError] = useState("");
  const inflight = useRef(false);
  const sentinel = useRef<HTMLDivElement>(null);

  if (head.data && head.data.path === path && acc.merged !== head.data) {
    const fresh = acc.key !== path || (acc.items.length > 0 && !head.data.items.some((item) => acc.items.some((known) => known.id === item.id)));
    setAcc(fresh
      ? { key: path, items: head.data.items, cursor: head.data.next_cursor, merged: head.data }
      : { ...acc, items: union(acc.items, head.data.items), merged: head.data });
  }
  const current = acc.key === path;
  const items = current ? acc.items : [];
  const cursor = current ? acc.cursor : null;

  const loadMore = useCallback(async () => {
    if (!cursor || inflight.current) return;
    inflight.current = true;
    setLoading(true);
    try {
      const page = await api<ActivityPage>(`${path}&cursor=${encodeURIComponent(cursor)}`);
      setMoreError("");
      setAcc((state) => state.key === path
        ? { ...state, items: union(state.items, page.items ?? []), cursor: page.next_cursor }
        : state);
    } catch (cause) {
      setMoreError(cause instanceof Error ? cause.message : "Request failed.");
    } finally {
      inflight.current = false;
      setLoading(false);
    }
  }, [cursor, path]);

  useEffect(() => {
    const target = sentinel.current;
    if (!target || !cursor || moreError) return;
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) void loadMore();
    });
    observer.observe(target);
    return () => observer.disconnect();
  }, [cursor, moreError, loadMore]);

  return <section className="panel stack">
    <div className="heading">
      <h2>Activity <span className="pill">{items.length}{cursor ? "+" : ""}</span></h2>
      <div className="actions">
        <select className="filter-input" aria-label="Activity project" value={brief} onChange={(event) => setBrief(event.target.value)}>
          <option value="">All projects</option>
          {(briefs.data?.briefs ?? []).map((item) => <option key={item.id} value={item.id}>{item.title}</option>)}
        </select>
        <select className="filter-input" aria-label="Activity severity" value={severity} onChange={(event) => setSeverity(event.target.value)}>
          <option value="">All severities</option>
          {severities.map((item) => <option key={item} value={item}>{item}</option>)}
        </select>
      </div>
    </div>
    <Feedback error={head.error} loading={head.data?.path !== path && !head.error} />
    {head.data?.path === path && !items.length && <p className="empty">No activity matches this view.</p>}
    {!!items.length && <div className="activity-feed" role="feed" aria-label="Activity">
      {items.map((item) => <article key={item.id} className="feed-row" data-severity={item.severity}>
        <span className="feed-icon" aria-hidden="true">{glyphs[item.icon] ?? glyphs.activity}</span>
        <div className="feed-body">
          <p>{item.text}</p>
          <div className="meta">
            <time dateTime={item.ts}>{stamp(item.ts)}</time>
            {item.targets.map((ref) => {
              const thread = threadOf(ref);
              return thread
                ? <button key={`${ref.kind}:${ref.id}`} type="button" className="text-button" onClick={() => openThread(thread)}>{ref.id}</button>
                : <span key={`${ref.kind}:${ref.id}`}>{ref.id}</span>;
            })}
          </div>
        </div>
        <span className="pill" data-severity={item.severity}>{item.severity}</span>
      </article>)}
      <div ref={sentinel} aria-hidden="true" />
    </div>}
    {moreError && <p className="error" role="alert">{moreError}</p>}
    {cursor && <button type="button" disabled={loading} onClick={() => { setMoreError(""); void loadMore(); }}>
      {loading ? "Loading…" : "Load more"}
    </button>}
  </section>;
}
