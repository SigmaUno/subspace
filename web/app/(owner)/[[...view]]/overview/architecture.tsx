"use client";

import { useContext, useMemo, useState } from "react";
import type { Component } from "../../../../lib/swarm";
import { statuses, type Task, type Thread } from "../../../../lib/types";
import { Clock, staleAt } from "../shared/components";
import { formatTime } from "../shared/format";
import { EmptyState, ResourceState } from "../shared/states";

const width = 216;
const strideX = 280;
const strideY = 100;

function layout(components: Component[]) {
  const known = new Set(components.map((component) => component.id));
  const placed = new Map<string, { x: number; y: number }>();
  let remaining = components;
  let depth = 0;
  while (remaining.length) {
    const ready = remaining.filter((component) => !component.depends_on?.some((id) => known.has(id) && !placed.has(id)));
    const row = ready.length ? ready : remaining;
    row.forEach((component, index) => placed.set(component.id, { x: 12 + depth * strideX, y: 12 + index * strideY }));
    const ids = new Set(row.map((component) => component.id));
    remaining = remaining.filter((component) => !ids.has(component.id));
    depth += 1;
  }
  return {
    placed,
    width: Math.max(250, ...Array.from(placed.values(), ({ x }) => x + width + 16)),
    height: Math.max(110, ...Array.from(placed.values(), ({ y }) => y + strideY)),
  };
}

function summarize(component: Component, tasks: Task[], now: number) {
  const matching = tasks.filter((task) => task.component_id === component.id || component.task_ids?.includes(task.id));
  const counts = matching.length ? Object.fromEntries(statuses.map(([status]) => [status, matching.filter((task) => task.status === status).length]))
    : component.status_counts ?? {};
  const progress = matching.length
    ? Math.round(matching.reduce((sum, task) => sum + (task.status === "done" ? 100 : task.progress_pct), 0) / matching.length)
    : component.progress_pct ?? 0;
  const stale = matching.length ? matching.filter((task) => task.status === "in_progress" && staleAt(task.last_heartbeat_at, now, task.lease_expires_at)).length
    : component.stale_count ?? 0;
  const owners = Array.from(new Set([...(component.owner_agent_ids ?? []), ...matching.flatMap((task) => task.assignee ? [task.assignee] : [])]));
  const activity = [component.last_activity_at, ...matching.map((task) => task.last_heartbeat_at)].filter(Boolean).sort().at(-1);
  return { counts, progress, stale, owners, activity };
}

export function Architecture({ components, tasks, loading, error, openThread, openChat }: {
  components: Component[]; tasks: Task[]; loading: boolean; error: string;
  openThread: (thread: Thread) => void; openChat: (target: string) => void;
}) {
  const graph = useMemo(() => layout(components), [components]);
  const [selected, setSelected] = useState<string>();
  const now = useContext(Clock);
  const current = components.find((component) => component.id === selected) ?? components[0];
  const summary = current && summarize(current, tasks, now);

  return <section className="panel stack">
    <div className="heading"><h2>Architecture</h2><span className="meta">{components.length} components</span></div>
    <ResourceState error={error} loading={loading} />
    {!loading && !components.length && <EmptyState>No architecture published for this brief.</EmptyState>}
    {!!components.length && <div className="graph-scroll" role="region" tabIndex={0} aria-label="Component dependency graph">
      <div className="graph" style={{ width: graph.width, height: graph.height }}>
        <svg width={graph.width} height={graph.height} aria-hidden="true">
          <defs><marker id="dependency-tip" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M 0 0 L 10 5 L 0 10 z" /></marker></defs>
          {components.flatMap((component) => {
            const to = graph.placed.get(component.id)!;
            return (component.depends_on ?? []).flatMap((dependency) => {
              const from = graph.placed.get(dependency);
              if (!from) return [];
              const pivot = (from.x + width + to.x) / 2;
              return <path key={`${dependency}:${component.id}`} className="edge" markerEnd="url(#dependency-tip)"
                d={`M ${from.x + width} ${from.y + 34} C ${pivot} ${from.y + 34}, ${pivot} ${to.y + 34}, ${to.x} ${to.y + 34}`} />;
            });
          })}
        </svg>
        {components.map((component) => {
          const position = graph.placed.get(component.id)!;
          const status = summarize(component, tasks, now);
          const attention = (status.counts.blocked ?? 0) + (status.counts.needs_owner ?? 0) + status.stale;
          return <button key={component.id} type="button" aria-pressed={current?.id === component.id}
            className={`component-node ${attention ? "needs-attention" : ""}`} style={{ left: position.x, top: position.y, width }}
            onClick={() => setSelected(component.id)}>
            <strong>{component.name}</strong>
            <span>{status.progress}% · {status.owners.length} owners{attention ? ` · ${attention} alerts` : ""}</span>
          </button>;
        })}
      </div>
    </div>}
    {current && summary && <div className="component-details stack">
      <div className="heading"><h3>{current.name}</h3><button type="button" onClick={() => openChat(`component:${current.id}`)}>Message component</button></div>
      {current.description && <p className="preserve">{current.description}</p>}
      {current.paths?.length ? <p className="meta">{current.paths.join(", ")}</p> : null}
      {!!current.depends_on?.length && <div className="actions"><span className="meta">Depends on</span>
        {current.depends_on.map((id) => <button key={id} type="button" onClick={() => setSelected(id)}>{components.find((item) => item.id === id)?.name || id}</button>)}
      </div>}
      <div className="actions">
        {statuses.filter(([status]) => summary.counts[status]).map(([status, label]) => <span key={status} className="pill" data-status={status}>{summary.counts[status]} {label.toLowerCase()}</span>)}
        {!!summary.stale && <span className="pill stale">{summary.stale} stale</span>}
      </div>
      <div className="actions"><span className="meta">Owners</span>{summary.owners.length ? summary.owners.map((id) => <button key={id} type="button" onClick={() => openThread({ kind: "agent", id })}>{id}</button>) : <span className="meta">Unassigned</span>}</div>
      {summary.activity && <p className="meta">Last activity {formatTime(summary.activity)}</p>}
    </div>}
  </section>;
}
