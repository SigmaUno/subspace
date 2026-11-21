"use client";

import { useContext, useState, type KeyboardEvent, type PointerEvent } from "react";
import { money, useResource } from "../../../../lib/client";
import type { Brief } from "../../../../lib/types";
import type { Timeseries, TimeseriesMetric } from "../../../../lib/timeseries-types";
import { Clock } from "../shared/components";
import { EmptyState, ResourceState } from "../shared/states";
import { count } from "../shared/format";

const ranges = [
  { id: "24h", name: "24 hours", ms: 86_400_000, bucket: "5m", bucketMs: 300_000 },
  { id: "7d", name: "7 days", ms: 7 * 86_400_000, bucket: "1h", bucketMs: 3_600_000 },
  { id: "30d", name: "30 days", ms: 30 * 86_400_000, bucket: "1d", bucketMs: 86_400_000 },
] as const;

type Range = (typeof ranges)[number];

const charts: { metric: TimeseriesMetric; title: string; kind: "bar" | "line"; format: (value: number) => string }[] = [
  { metric: "tasks_created", title: "Tasks created", kind: "bar", format: (value) => String(value) },
  { metric: "tasks_done", title: "Tasks done", kind: "bar", format: (value) => String(value) },
  { metric: "review_rounds", title: "Review rounds", kind: "bar", format: (value) => String(value) },
  { metric: "active_agents", title: "Active agents", kind: "line", format: (value) => String(value) },
  { metric: "cost_eur", title: "Cost", kind: "line", format: money },
  { metric: "tokens", title: "Tokens", kind: "line", format: (value) => count.format(value) },
];

const frame = { width: 320, height: 120, left: 6, right: 6, top: 8, bottom: 8 };
const innerWidth = frame.width - frame.left - frame.right;
const innerHeight = frame.height - frame.top - frame.bottom;

const stamp = (time: number, range: Range) => new Date(time).toLocaleString([], range.id === "30d"
  ? { month: "short", day: "numeric" }
  : { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });

interface Shared {
  range: Range;
  since: string;
  briefID: string;
  revision: number;
  hover: number | null;
  setHover: (time: number | null) => void;
}

function TrendChart({ metric, title, kind, format, range, since, briefID, revision, hover, setHover }: Shared & (typeof charts)[number]) {
  const query = new URLSearchParams({ metric, bucket: range.bucket, since });
  if (briefID) query.set("brief_id", briefID);
  const series = useResource<Timeseries>(`timeseries?${query}`, revision);
  const points = series.data?.points ?? [];
  const times = points.map((point) => Date.parse(point.t));
  const high = Math.max(...points.map((point) => point.v), 0) || 1;
  const slot = innerWidth / Math.max(points.length, 1);
  const x = (index: number) => frame.left + (kind === "bar" ? slot * (index + 0.5) : points.length > 1 ? innerWidth * index / (points.length - 1) : innerWidth / 2);
  const y = (value: number) => frame.top + (1 - value / high) * innerHeight;
  const index = hover === null ? -1 : times.indexOf(hover);
  const total = points.reduce((sum, point) => sum + point.v, 0);

  function at(event: PointerEvent<SVGSVGElement>) {
    const box = event.currentTarget.getBoundingClientRect();
    if (!points.length || !box.width) return;
    const position = (event.clientX - box.left) / box.width * frame.width;
    const nearest = kind === "bar"
      ? Math.floor((position - frame.left) / slot)
      : Math.round((position - frame.left) / innerWidth * (points.length - 1));
    setHover(times[Math.min(points.length - 1, Math.max(0, nearest))]);
  }

  function key(event: KeyboardEvent<SVGSVGElement>) {
    if (!points.length) return;
    const step = event.key === "ArrowLeft" ? -1 : event.key === "ArrowRight" ? 1 : 0;
    if (event.key === "Escape") setHover(null);
    if (!step) return;
    event.preventDefault();
    setHover(times[Math.min(points.length - 1, Math.max(0, (index < 0 ? points.length : index) + step))]);
  }

  const shown = index >= 0 ? points[index].v : undefined;
  return <section className="panel stack trend">
    <div className="heading"><h2>{title}</h2>
      <strong>{shown === undefined ? format(metric === "active_agents" ? Math.max(...points.map((point) => point.v), 0) : total) : format(shown)}</strong></div>
    <p className="meta">{shown === undefined
      ? metric === "active_agents" ? "Peak in range" : "Total in range"
      : stamp(times[index], range)}</p>
    <ResourceState error={series.error} loading={!series.data && !series.error} retry={series.refresh} />
    {series.data && (points.some((point) => point.v > 0)
      ? <svg viewBox={`0 0 ${frame.width} ${frame.height}`} role="img" tabIndex={0} aria-label={`${title} per ${range.bucket} bucket over ${range.name}`}
        onPointerMove={at} onPointerLeave={() => setHover(null)} onKeyDown={key} onBlur={() => setHover(null)}>
        <line className="axis" x1={frame.left} x2={frame.left + innerWidth} y1={frame.top + innerHeight} y2={frame.top + innerHeight} />
        {kind === "bar"
          ? points.map((point, i) => <rect key={point.t} className={i === index ? "bar active" : "bar"} x={x(i) - slot * 0.4} width={Math.max(slot * 0.8, 0.5)}
            y={y(point.v)} height={frame.top + innerHeight - y(point.v)} />)
          : <polyline className="series" points={points.map((point, i) => `${x(i).toFixed(1)},${y(point.v).toFixed(1)}`).join(" ")} />}
        {index >= 0 && <>
          <line className="crosshair" x1={x(index)} x2={x(index)} y1={frame.top} y2={frame.top + innerHeight} />
          {kind === "line" && <circle className="point" cx={x(index)} cy={y(points[index].v)} r={3.5} />}
        </>}
      </svg>
      : <EmptyState>No activity in this range.</EmptyState>)}
  </section>;
}

export function TrendsView({ revision }: { revision: number }) {
  const now = useContext(Clock);
  const [rangeID, setRangeID] = useState<Range["id"]>("7d");
  const [briefID, setBriefID] = useState("");
  const [hover, setHover] = useState<number | null>(null);
  const briefs = useResource<{ briefs: Brief[] }>("briefs", revision);
  const range = ranges.find((item) => item.id === rangeID)!;
  const since = now ? new Date(Math.floor((now - range.ms) / range.bucketMs) * range.bucketMs).toISOString() : "";
  return <>
    <div className="heading"><h1>Trends</h1>
      <div className="actions">
        <div className="actions" role="group" aria-label="Range">
          {ranges.map((item) => <button key={item.id} type="button" className={item.id === rangeID ? "primary" : ""}
            aria-pressed={item.id === rangeID} onClick={() => { setRangeID(item.id); setHover(null); }}>{item.id}</button>)}
        </div>
        <label>Brief<select value={briefID} onChange={(event) => { setBriefID(event.target.value); setHover(null); }}>
          <option value="">All briefs</option>
          {briefs.data?.briefs.map((brief) => <option key={brief.id} value={brief.id}>{brief.id} · {brief.title}</option>)}
        </select></label>
      </div>
    </div>
    {since && <div className="trend-grid">
      {charts.map((chart) => <TrendChart key={`${chart.metric}:${range.id}:${briefID}`} {...chart}
        range={range} since={since} briefID={briefID} revision={revision} hover={hover} setHover={setHover} />)}
    </div>}
  </>;
}
