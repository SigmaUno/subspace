"use client";

import { label, optionalResource, useResource } from "../../../lib/client";
import type { Insights } from "../../../lib/insights-types";
import { EntityLink } from "./components";
import { utc as since } from "./format";
import { EmptyState, ResourceState } from "./states";

const severityClass = { warning: "paused", high: "revoked" } as const;


export function InsightsPanel({ id, revision }: { id: string; revision: number }) {
  const feed = useResource<Insights>(`agents/${encodeURIComponent(id)}/insights`, revision);
  const insights = feed.data;
  return <section className="panel stack">
    <div className="heading"><h2>Insights</h2>
      {insights && <span className="meta">{insights.high} high · {insights.warning} warning</span>}</div>
    <ResourceState error={feed.error} loading={!insights && !feed.error} retry={feed.refresh} />
    {insights && !insights.findings.length && <EmptyState>No behavior findings in the last 24 hours.</EmptyState>}
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
    {insights && insights.findings.length > 0 && <EntityLink kind="agent" id={insights.findings[0].agent_id} />}
  </div>;
}
