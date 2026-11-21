"use client";

import Link from "next/link";
import { money, label, useResource } from "../../../lib/client";
import type { BriefReport, TaskReport } from "../../../lib/report-types";
import { useLive } from "./live";
import { PageShell } from "./shell";
import { EmptyState, ResourceState } from "./states";

const closed = new Set(["done", "cancelled"]);

function duration(seconds: number) {
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return hours < 48 ? `${hours}h ${minutes % 60}m` : `${Math.floor(hours / 24)}d ${hours % 24}h`;
}

function entries(values: Record<string, number> | null) {
  return Object.entries(values ?? {}).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
}

function markdown(report: BriefReport) {
  const lines = [
    `# ${report.brief_id} — ${report.title}`,
    "",
    `Generated ${report.generated_at}`,
    "",
    "## Tasks",
    "",
    "| Task | Title | Status | Agent | Queue | Work | Review | Blocked | Rounds | Cost € |",
    "|---|---|---|---|--:|--:|--:|--:|--:|--:|",
    ...(report.tasks ?? []).map((task) => `| ${task.id} | ${task.title.replaceAll("|", "\\|")} | ${task.status} | ${task.assignee || "unassigned"} | ` +
      `${duration(task.queue_seconds)} | ${duration(task.work_seconds)} | ${duration(task.review_seconds)} | ${duration(task.blocked_seconds)} | ` +
      `${task.review_rounds} | ${task.cost_eur.toFixed(2)} |`),
    "",
    "## Cost",
    "",
    `Total: ${report.cost.total_eur.toFixed(2)} €`,
    "",
    ...entries(report.cost.by_provider).map(([name, amount]) => `- ${name}: ${amount.toFixed(2)} €`),
    "",
    "## Flow",
    "",
    `- Blocked time: ${duration(report.blocked_seconds)}`,
    `- Done: ${report.throughput.done} in ${report.throughput.window_hours.toFixed(2)} h (${report.throughput.per_hour.toFixed(2)} per hour)`,
  ];
  const path = report.critical_path.task_ids ?? [];
  if (path.length) lines.push(`- Critical path: ${path.join(" → ")} (${duration(report.critical_path.seconds)})`);
  return lines.join("\n") + "\n";
}

type Segment = { status: string; from: number; to: number };

function segments(task: TaskReport, generated: number): Segment[] {
  const out: Segment[] = [];
  const steps = task.timeline ?? [];
  let status = steps[0]?.from ?? task.status;
  let since = Date.parse(task.created_at);
  for (const step of steps) {
    const at = Date.parse(step.at);
    out.push({ status, from: since, to: at });
    status = step.to;
    since = at;
  }
  if (!closed.has(status)) out.push({ status, from: since, to: Math.max(since, task.done_at ? Date.parse(task.done_at) : generated) });
  return out.filter((segment) => segment.to > segment.from);
}

const gantt = { width: 960, left: 64, right: 16, top: 8, row: 26, axis: 28 };

function Gantt({ report }: { report: BriefReport }) {
  const tasks = report.tasks ?? [];
  const generated = Date.parse(report.generated_at);
  const rows = tasks.map((task) => ({ task, parts: segments(task, generated) }));
  const start = Math.min(...tasks.map((task) => Date.parse(task.created_at)));
  const end = Math.max(start + 1, ...rows.flatMap((row) => row.parts.map((part) => part.to)));
  const critical = new Set(report.critical_path.task_ids ?? []);
  const inner = gantt.width - gantt.left - gantt.right;
  const height = gantt.top + rows.length * gantt.row + gantt.axis;
  const x = (time: number) => gantt.left + ((time - start) / (end - start)) * inner;
  const statuses = [...new Set(rows.flatMap((row) => row.parts.map((part) => part.status)))];
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((fraction) => start + fraction * (end - start));

  return <div className="stack">
    <svg className="hillclimb-chart" viewBox={`0 0 ${gantt.width} ${height}`} role="img" aria-label="Task timeline">
      {rows.map(({ task, parts }, index) => {
        const y = gantt.top + index * gantt.row;
        const hot = critical.has(task.id);
        return <g key={task.id}>
          {hot && <rect x={0} y={y} width={gantt.width} height={gantt.row} style={{ fill: "var(--warn-soft)", stroke: "var(--warn-border)" }} />}
          <text className="tick" x={gantt.left - 8} y={y + gantt.row / 2} textAnchor="end" dominantBaseline="middle"
            style={hot ? { fill: "var(--warn)", fontWeight: 700 } : undefined}>{task.id}</text>
          {parts.map((part) => <rect key={`${part.status}-${part.from}`} data-status={part.status} style={{ fill: "var(--s-fg)", opacity: .85 }}
            x={x(part.from)} y={y + 5} width={Math.max(1, x(part.to) - x(part.from))} height={gantt.row - 10} rx={2}>
            <title>{task.id} {task.title} · {label(part.status)} · {duration((part.to - part.from) / 1000)}</title>
          </rect>)}
        </g>;
      })}
      <line className="axis" x1={gantt.left} x2={gantt.left + inner} y1={height - gantt.axis} y2={height - gantt.axis} />
      {ticks.map((time) => <text key={time} className="tick" x={x(time)} y={height - 8}
        textAnchor={time === start ? "start" : time === end ? "end" : "middle"}>
        {new Date(time).toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })}
      </text>)}
    </svg>
    <div className="actions">
      {statuses.map((status) => <span key={status} className="pill" data-status={status}>{label(status)}</span>)}
      {critical.size > 0 && <span className="meta">Highlighted rows are on the critical path</span>}
    </div>
  </div>;
}

const bars = { width: 560, left: 150, right: 72, row: 24 };

function CostBars({ title, values, limit }: { title: string; values: Record<string, number> | null; limit?: number }) {
  const rows = entries(values).slice(0, limit);
  const high = Math.max(...rows.map(([, amount]) => amount)) || 1;
  const inner = bars.width - bars.left - bars.right;
  return <section className="panel stack">
    <h3>{title}</h3>
    {rows.length ? <svg className="hillclimb-chart" viewBox={`0 0 ${bars.width} ${rows.length * bars.row}`} role="img" aria-label={title}>
      {rows.map(([name, amount], index) => {
        const y = index * bars.row;
        return <g key={name}>
          <text className="tick" x={bars.left - 8} y={y + bars.row / 2} textAnchor="end" dominantBaseline="middle">{name}</text>
          <rect style={{ fill: "var(--action)" }} x={bars.left} y={y + 4} width={Math.max(1, (amount / high) * inner)} height={bars.row - 8} rx={2} />
          <text className="tick" x={bars.left + (amount / high) * inner + 6} y={y + bars.row / 2} dominantBaseline="middle">{money(amount)}</text>
        </g>;
      })}
    </svg> : <EmptyState>No cost recorded.</EmptyState>}
  </section>;
}

const spark = { width: 360, height: 64, pad: 6 };

function Sparkline({ report }: { report: BriefReport }) {
  const hourly = report.throughput.hourly ?? [];
  const high = Math.max(...hourly.map((bucket) => bucket.done), 1);
  const x = (index: number) => hourly.length > 1 ? spark.pad + (index / (hourly.length - 1)) * (spark.width - 2 * spark.pad) : spark.width / 2;
  const y = (done: number) => spark.height - spark.pad - (done / high) * (spark.height - 2 * spark.pad);
  const points = hourly.map((bucket, index) => `${x(index)},${y(bucket.done)}`).join(" ");
  return <svg className="hillclimb-chart" viewBox={`0 0 ${spark.width} ${spark.height}`} role="img" aria-label="Tasks completed per hour">
    <polyline className="series" points={points} />
    {hourly.map((bucket, index) => <circle key={bucket.hour} className="point" cx={x(index)} cy={y(bucket.done)} r={2.5}>
      <title>{new Date(bucket.hour).toLocaleString()}: {bucket.done} done</title>
    </circle>)}
  </svg>;
}

function Tile({ name, value, note, hot }: { name: string; value: string; note?: string; hot?: boolean }) {
  return <div className={`panel stat ${hot ? "needs-attention" : ""}`}>
    <span className="meta">{name}</span>
    <strong>{value}</strong>
    {note && <span className="meta">{note}</span>}
  </div>;
}

export function ReportPage({ id }: { id: string }) {
  const { revisions } = useLive("all");
  const report = useResource<BriefReport>(`briefs/${encodeURIComponent(id)}/report`, revisions.board);
  const data = report.data;
  const path = data?.critical_path.task_ids ?? [];
  const done = data?.counts?.done ?? 0;
  const briefPath = `/briefs/${encodeURIComponent(id)}`;

  return <PageShell section="briefs">
    <div className="heading">
      <div>
        <div className="meta"><Link href={briefPath}>{id}</Link> · report</div>
        <h1>{data?.title ?? "Brief report"}</h1>
      </div>
      {data && <div className="actions">
        <a href={`data:text/markdown;charset=utf-8,${encodeURIComponent(markdown(data))}`} download="report.md">Download report.md</a>
      </div>}
    </div>
    <ResourceState error={report.error} loading={!data} retry={report.refresh} rows={4} />
    {data && <>
      <div className="stat-grid">
        <Tile name="Tasks" value={String(data.total)} note={entries(data.counts).map(([status, count]) => `${count} ${label(status)}`).join(" · ")} />
        <Tile name="Done" value={`${done} / ${data.total}`} />
        <Tile name="Cost" value={money(data.cost.total_eur)} />
        <Tile name="Blocked time" value={duration(data.blocked_seconds)} hot={data.blocked_seconds > 0} />
        <Tile name="Throughput" value={`${data.throughput.per_hour.toFixed(2)} / h`} note={`${data.throughput.done} done in ${data.throughput.window_hours.toFixed(1)} h`} />
        <Tile name="Critical path" value={duration(data.critical_path.seconds)} note={path.join(" → ") || undefined} />
      </div>

      <section className="panel stack">
        <h2>Timeline</h2>
        {data.tasks?.length ? <Gantt report={data} /> : <EmptyState>This brief has no tasks.</EmptyState>}
      </section>

      <section className="stack">
        <h2>Cost breakdown</h2>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(22rem, 1fr))", gap: ".8rem" }}>
          <CostBars title="By provider" values={data.cost.by_provider} />
          <CostBars title="By agent" values={data.cost.by_agent} />
          <CostBars title="By task" values={data.cost.by_task} limit={10} />
        </div>
      </section>

      <section className="panel stack">
        <h2>Throughput</h2>
        {data.throughput.done ? <Sparkline report={data} /> : <EmptyState>No tasks completed yet.</EmptyState>}
      </section>
    </>}
  </PageShell>;
}
