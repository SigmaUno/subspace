"use client";

import { useState } from "react";
import { api, label, useResource, useSubmit, value } from "../../../../lib/client";
import type { HillclimbJob, HillclimbPoint, HillclimbSeries } from "../../../../lib/hillclimb-types";
import { EntityLink, Feedback } from "../shared/components";
import { EmptyState, ResourceState } from "../shared/states";
import { PageShell } from "../shell";

function CreateJobForm({ created }: { created: (job: HillclimbJob) => void }) {
  const form = useSubmit();
  return <form className="panel stack" data-write onSubmit={(event) => form.submit(event, async (fields) => {
    const target = value(fields, "target");
    const job = await api<HillclimbJob>("hillclimb/jobs", "POST", {
      brief_id: value(fields, "brief_id"),
      repo: value(fields, "repo"),
      metric_cmd: value(fields, "metric_cmd"),
      direction: value(fields, "direction"),
      target: target === "" ? null : Number(target),
      budget: Number(value(fields, "budget") || 0),
      plateau_n: Number(value(fields, "plateau_n") || 0),
    });
    created(job);
  })}>
    <h2>New hillclimb job</h2>
    <div className="form-grid">
      <label>Brief<input name="brief_id" placeholder="B-1" required /></label>
      <label>Repo<input name="repo" placeholder="SigmaUno/foo" required /></label>
      <label>Metric command<input name="metric_cmd" placeholder="make bench" required /></label>
      <label>Direction<select name="direction" defaultValue="min">
        <option value="min">Minimise</option><option value="max">Maximise</option>
      </select></label>
      <label>Target (optional)<input name="target" type="number" step="any" /></label>
      <label>Attempt budget (0 = unlimited)<input name="budget" type="number" min="0" defaultValue="0" required /></label>
      <label>Plateau after attempts (0 = off)<input name="plateau_n" type="number" min="0" defaultValue="0" required /></label>
    </div>
    <Feedback error={form.error} />
    <div><button className="primary" disabled={form.busy}>Create job</button></div>
  </form>;
}

const chart = { width: 720, height: 300, left: 56, right: 16, top: 16, bottom: 32 };
const innerWidth = chart.width - chart.left - chart.right;
const innerHeight = chart.height - chart.top - chart.bottom;
const formatValue = (amount: number) => Number(amount.toPrecision(4)).toString();

function LossChart({ job, points }: { job: HillclimbJob; points: HillclimbPoint[] }) {
  const plotted = points.filter((point): point is HillclimbPoint & { value: number } => point.value !== null);
  if (!plotted.length) return <EmptyState>No successful measurements yet.</EmptyState>;

  const scale = plotted.map((point) => point.value);
  if (job.target !== null) scale.push(job.target);
  let low = Math.min(...scale);
  let high = Math.max(...scale);
  if (low === high) {
    low -= 1;
    high += 1;
  }
  const x = (n: number) => chart.left + (points.length > 1 ? ((n - 1) / (points.length - 1)) * innerWidth : innerWidth / 2);
  const y = (amount: number) => chart.top + (1 - (amount - low) / (high - low)) * innerHeight;
  const best = plotted.find((point) => point.attempt_id === job.best_attempt_id);
  const line = plotted.map((point) => `${x(point.n).toFixed(1)},${y(point.value).toFixed(1)}`).join(" ");

  return <svg className="hillclimb-chart" viewBox={`0 0 ${chart.width} ${chart.height}`} role="img" aria-label={`${job.id} attempts over time`}>
    <line className="axis" x1={chart.left} x2={chart.left} y1={chart.top} y2={chart.top + innerHeight} />
    <line className="axis" x1={chart.left} x2={chart.left + innerWidth} y1={chart.top + innerHeight} y2={chart.top + innerHeight} />
    {[high, (high + low) / 2, low].map((tick) => <text key={tick} className="tick" x={chart.left - 8} y={y(tick)} textAnchor="end" dominantBaseline="middle">{formatValue(tick)}</text>)}
    <text className="tick" x={x(1)} y={chart.height - 8} textAnchor="middle">1</text>
    {points.length > 1 && <text className="tick" x={x(points.length)} y={chart.height - 8} textAnchor="middle">{points.length}</text>}
    {job.target !== null && <>
      <line className="target" x1={chart.left} x2={chart.left + innerWidth} y1={y(job.target)} y2={y(job.target)} />
      <text className="target-label" x={chart.left + innerWidth} y={y(job.target) - 6} textAnchor="end">target {formatValue(job.target)}</text>
    </>}
    <polyline className="series" points={line} />
    {plotted.map((point) => <circle key={point.attempt_id} className="point" cx={x(point.n)} cy={y(point.value)} r={3.5}>
      <title>#{point.n} {point.agent_id}: {formatValue(point.value)}</title>
    </circle>)}
    {best && <circle className="best" cx={x(best.n)} cy={y(best.value)} r={8}>
      <title>best #{best.n}: {formatValue(best.value)}</title>
    </circle>}
  </svg>;
}

function HillclimbView() {
  const jobs = useResource<{ jobs: HillclimbJob[] }>("hillclimb/jobs", 0);
  const [selected, setSelected] = useState<string>();
  const [showForm, setShowForm] = useState(false);
  const active = selected ?? jobs.data?.jobs[0]?.id;
  const series = useResource<HillclimbSeries>(active ? `hillclimb/jobs/${encodeURIComponent(active)}/series` : null, 0);
  const current = series.data?.job.id === active ? series.data : undefined;

  return <>
    <div className="heading"><h1>Hillclimb</h1>
      <button className="primary" data-write onClick={() => setShowForm(!showForm)}>{showForm ? "Close form" : "New job"}</button></div>
    {showForm && <CreateJobForm created={(job) => { setShowForm(false); setSelected(job.id); jobs.refresh(); }} />}
    <ResourceState error={jobs.error} loading={!jobs.data} retry={jobs.refresh} rows={3} />
    {jobs.data && !jobs.data.jobs.length && <EmptyState>No hillclimb jobs yet.</EmptyState>}
    {!!jobs.data?.jobs.length && <div className="table-wrap">
      <table>
        <thead><tr><th>Job</th><th>Brief</th><th>Repo</th><th>Direction</th><th>Status</th><th>Best</th><th>Attempts</th></tr></thead>
        <tbody>{jobs.data.jobs.map((job) => (
          <tr key={job.id}>
            <th scope="row"><button className="text-button" aria-pressed={job.id === active} onClick={() => setSelected(job.id)}>{job.id}</button></th>
            <td><EntityLink kind="brief" id={job.brief_id} /></td>
            <td>{job.repo}</td>
            <td>{job.direction}{job.target !== null && <div className="meta">target {formatValue(job.target)}</div>}</td>
            <td><span className="pill">{job.status}</span>{job.stop_reason && <div className="meta">{label(job.stop_reason)}</div>}</td>
            <td>{job.best !== null ? formatValue(job.best) : "—"}</td>
            <td>{job.attempts}{job.budget > 0 && <span className="meta"> / {job.budget}</span>}</td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
    {active && <section className="panel stack">
      <div className="heading"><h2>{active} loss curve</h2>
        {current && <span className="meta">{current.job.metric_cmd} · since best {current.job.since_best}</span>}</div>
      <ResourceState error={series.error} loading={!current} retry={series.refresh} rows={4} />
      {current && <LossChart job={current.job} points={current.points} />}
    </section>}
  </>;
}

export function HillclimbPage() {
  return <PageShell section="hillclimb">
    <HillclimbView />
  </PageShell>;
}
