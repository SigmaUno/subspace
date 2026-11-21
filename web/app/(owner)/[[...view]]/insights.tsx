"use client";

import Link from "next/link";
import { label, optionalResource, useResource } from "../../../lib/client";
import type { Insights } from "../../../lib/insights-types";
import { Feedback } from "./components";

const severityClass = { warning: "paused", high: "revoked" } as const;

const since = (iso: string) => `${new Date(iso).toLocaleString([], { dateStyle: "short", timeStyle: "short", timeZone: "UTC" })} UTC`;

export function InsightsPanel({ id, revision }: { id: string; revision: number }) {
  const feed = useResource<Insights>(`agents/${encodeURIComponent(id)}/insights`, revision);
  const insights = feed.data;
  return <section className="panel stack">
    <div className="heading"><h2>Insights</h2>
      {insights && <span className="meta">{insights.high} high · {insights.warning} warning</span>}</div>
    <Feedback error={feed.error} loading={!insights && !feed.error} />
    {insights && !insights.findings.length && <p className="empty">No behavior findings in the last 24 hours.</p>}
    {insights?.findings.map((finding) => <article key={finding.id} className="stack">
      <div className="actions">
        <span className={`pill ${severityClass[finding.severity]}`}>{finding.severity}</span>
        <strong>{label(finding.rule)}</strong>
      </div>
      <p>{finding.summary}</p>
      <p className="meta">Since {since(finding.since)}
        {finding.task_ids.length > 0 && ` · tasks ${finding.task_ids.join(", ")}`}
        {` · evidence ${finding.evidence.map((item) => `${item.kind} ${item.id}`).join(", ")}`}</p>
    </article>)}
  </section>;
}

export function InsightsTile({ revision }: { revision: number }) {
  const feed = useResource("insights", revision, optionalResource<Insights>);
  const insights = feed.data;
  if (insights === null) return null;
  return <div className={`panel ${insights?.high ? "needs-attention" : ""}`}>
    <span>Agent insights</span>
    <strong>{insights ? `${insights.high} high · ${insights.warning} warning` : "…"}</strong>
    {insights && insights.findings.length > 0 && <Link href={`/agents/${encodeURIComponent(insights.findings[0].agent_id)}`}>{insights.findings[0].agent_id}</Link>}
  </div>;
}
