"use client";

import Link from "next/link";
import { useContext, useState } from "react";
import { money, useResource } from "../../../lib/client";
import type { CostBreakdown, CostDimension, CostGroup } from "../../../lib/costs-types";
import type { Brief } from "../../../lib/types";
import { Clock } from "./components";
import { EmptyState, ResourceState, Skeleton } from "./states";

const dimensions: { id: CostDimension; name: string }[] = [
  { id: "brief", name: "Brief" },
  { id: "task", name: "Task" },
  { id: "component", name: "Component" },
  { id: "agent", name: "Agent" },
  { id: "kind", name: "Kind" },
  { id: "model", name: "Model" },
  { id: "provider", name: "Provider" },
  { id: "role", name: "Role" },
  { id: "day", name: "Day" },
];

const ranges = [
  { id: "24h", ms: 86_400_000 },
  { id: "7d", ms: 7 * 86_400_000 },
  { id: "30d", ms: 30 * 86_400_000 },
] as const;

const slotMs = 300_000;
const maxBars = 12;
const count = new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 });
const chart = { width: 720, row: 28, left: 150, right: 80, top: 8, bottom: 8 };
const barWidth = chart.width - chart.left - chart.right;

const stackFor = (by: CostDimension): CostDimension => by === "provider" ? "model" : "provider";
const shorten = (text: string) => text.length > 20 ? `${text.slice(0, 19)}…` : text;
const color = (index: number) => `hsl(${(index * 47 + 210) % 360} 55% 48%)`;

function StackedBars({ groups, colors, by, stack }: { groups: CostGroup[]; colors: Map<string, string>; by: CostDimension; stack: CostDimension }) {
  const shown = groups.slice(0, maxBars);
  const high = Math.max(...shown.map((group) => group.cost_eur), 0) || 1;
  const height = chart.top + shown.length * chart.row + chart.bottom;
  return <svg className="hillclimb-chart" viewBox={`0 0 ${chart.width} ${height}`} role="img" aria-label={`Cost by ${by}, stacked by ${stack}`}>
    <line className="axis" x1={chart.left} x2={chart.left} y1={chart.top} y2={height - chart.bottom} />
    {shown.map((group, row) => {
      const y = chart.top + row * chart.row;
      const parts: { key: string; cost_eur: number }[] = group.split?.length ? group.split : [{ key: "", cost_eur: group.cost_eur }];
      let offset = 0;
      return <g key={group.key}>
        <text className="tick" x={chart.left - 8} y={y + chart.row / 2} textAnchor="end" dominantBaseline="middle">{shorten(group.key || "—")}</text>
        {parts.map((part) => {
          const width = part.cost_eur / high * barWidth;
          const x = chart.left + offset;
          offset += width;
          return <rect key={part.key} x={x} y={y + 4} width={Math.max(width, 0)} height={chart.row - 8} fill={colors.get(part.key) ?? "var(--action)"}>
            <title>{group.key || "—"}{part.key ? ` · ${part.key}` : ""}: {money(part.cost_eur)}</title>
          </rect>;
        })}
        <text className="tick" x={chart.left + offset + 6} y={y + chart.row / 2} dominantBaseline="middle">{money(group.cost_eur)}</text>
      </g>;
    })}
  </svg>;
}

function Legend({ colors }: { colors: Map<string, string> }) {
  return <ul className="actions" aria-label="Stack legend">
    {[...colors].map(([key, fill]) => <li key={key} className="meta" style={{ listStyle: "none" }}>
      <span aria-hidden="true" style={{ display: "inline-block", width: ".7rem", height: ".7rem", marginRight: ".35rem", background: fill }} />{key || "—"}
    </li>)}
  </ul>;
}

function Key({ by, id, briefID }: { by: CostDimension; id: string; briefID: string }) {
  if (!id) return <>—</>;
  const href = by === "brief" ? `/briefs/${encodeURIComponent(id)}`
    : by === "task" ? `/tasks/${encodeURIComponent(id)}`
    : by === "agent" ? `/agents/${encodeURIComponent(id)}`
    : by === "component" && briefID ? `/briefs/${encodeURIComponent(briefID)}/architecture`
    : "";
  return href ? <Link href={href}>{id}</Link> : <>{id}</>;
}

function CostTable({ breakdown, by, briefID, drill }: { breakdown: CostBreakdown; by: CostDimension; briefID: string; drill: (id: string) => void }) {
  const groups = breakdown.groups ?? [];
  if (!groups.length) return <EmptyState>No costs recorded in this range.</EmptyState>;
  const total = breakdown.total.cost_eur || 1;
  return <div className="table-wrap">
    <table>
      <thead><tr><th>{dimensions.find((dimension) => dimension.id === by)!.name}</th><th>Cost</th><th>Share</th><th>Requests</th><th>Tokens in</th><th>Tokens out</th><th>Cached</th>{by === "brief" && <th>Drill down</th>}</tr></thead>
      <tbody>{groups.map((group) => (
        <tr key={group.key}>
          <th scope="row"><Key by={by} id={group.key} briefID={briefID} /></th>
          <td>{money(group.cost_eur)}</td>
          <td>{Math.round(group.cost_eur / total * 100)}%</td>
          <td>{count.format(group.requests)}</td>
          <td>{count.format(group.tokens_in)}</td>
          <td>{count.format(group.tokens_out)}</td>
          <td>{count.format(group.cache_read + group.cache_write)}</td>
          {by === "brief" && <td>{group.key && <button type="button" onClick={() => drill(group.key)}>Tasks</button>}</td>}
        </tr>
      ))}</tbody>
    </table>
  </div>;
}

export function CostsView({ revision }: { revision: number }) {
  const now = useContext(Clock);
  const [by, setBy] = useState<CostDimension>("brief");
  const [rangeID, setRangeID] = useState<(typeof ranges)[number]["id"]>("7d");
  const [briefID, setBriefID] = useState("");
  const briefs = useResource<{ briefs: Brief[] }>("briefs", revision);
  const range = ranges.find((item) => item.id === rangeID)!;
  const stack = stackFor(by);
  const since = now ? new Date(Math.floor((now - range.ms) / slotMs) * slotMs).toISOString() : "";
  const query = new URLSearchParams({ group_by: by, split_by: stack, since });
  if (briefID) query.set("brief_id", briefID);
  const costs = useResource<CostBreakdown>(since ? `costs?${query}` : null, revision);
  const current = costs.data?.group_by === by && (costs.data.brief_id ?? "") === briefID ? costs.data : undefined;
  const groups = current?.groups ?? [];
  const keys = [...new Set(groups.slice(0, maxBars).flatMap((group) => (group.split ?? []).map((part) => part.key)))].sort();
  const colors = new Map<string, string>(keys.map((key, index): [string, string] => [key, color(index)]));

  return <>
    <div className="heading"><h1>Costs</h1>
      <div className="actions">
        {current && <span className="pill">{money(current.total.cost_eur)} in {range.id}</span>}
        <div className="actions" role="group" aria-label="Range">
          {ranges.map((item) => <button key={item.id} type="button" className={item.id === rangeID ? "primary" : undefined}
            aria-pressed={item.id === rangeID} onClick={() => setRangeID(item.id)}>{item.id}</button>)}
        </div>
        <label>Brief<select value={briefID} onChange={(event) => setBriefID(event.target.value)}>
          <option value="">All briefs</option>
          {briefs.data?.briefs?.map((brief) => <option key={brief.id} value={brief.id}>{brief.id} · {brief.title}</option>)}
        </select></label>
      </div>
    </div>
    <ResourceState error={costs.error} retry={costs.refresh} />

    <section className="stack">
      <div className="heading"><h2>Cost by {by}</h2>
        <div className="actions" role="group" aria-label="Group costs by">
          {dimensions.map((dimension) => <button key={dimension.id} type="button" aria-pressed={dimension.id === by}
            className={dimension.id === by ? "primary" : undefined} onClick={() => setBy(dimension.id)}>{dimension.name}</button>)}
        </div>
      </div>
      {!current && !costs.error && <Skeleton rows={3} />}
      {current && groups.length > 0 && <div className="panel stack">
        <StackedBars groups={groups} colors={colors} by={by} stack={stack} />
        <Legend colors={colors} />
      </div>}
      {current && <CostTable breakdown={current} by={by} briefID={briefID} drill={(id) => { setBriefID(id); setBy("task"); }} />}
    </section>
  </>;
}
