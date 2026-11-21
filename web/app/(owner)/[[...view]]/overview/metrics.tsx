"use client";

import { useState } from "react";
import { optionalResource, useResource } from "../../../../lib/client";
import type { HillclimbJob, HillclimbPoint, HillclimbSeries } from "../../../../lib/hillclimb-types";
import { AttemptList } from "../hillclimb/attempts";
import { EmptyState, ResourceState } from "../shared/states";

const number = new Intl.NumberFormat("en", { maximumSignificantDigits: 6 });

function LossCurve({ attempts, job }: { attempts: HillclimbPoint[]; job: HillclimbJob }) {
  const measured = attempts.filter((item): item is HillclimbPoint & { value: number } => item.value !== null && Number.isFinite(item.value));
  if (!measured.length) return <EmptyState>No measured attempts.</EmptyState>;
  const values = measured.map((item) => item.value);
  const min = Math.min(...values, job.target ?? values[0]);
  const max = Math.max(...values, job.target ?? values[0]);
  const times = measured.map((item) => Date.parse(item.measured_at));
  const useTime = times.every(Number.isFinite) && times.at(-1)! > times[0];
  const x = (index: number) => 62 + 548 * (useTime ? (times[index] - times[0]) / (times.at(-1)! - times[0]) : index / Math.max(1, measured.length - 1));
  const y = (value: number) => min === max ? 90 : 158 - 132 * (value - min) / (max - min);
  const best = measured.reduce<number[]>((history, item, index) => {
    const previous = history[index - 1] ?? item.value;
    history.push(job.direction === "max" ? Math.max(previous, item.value) : Math.min(previous, item.value));
    return history;
  }, []);
  const ticks = min === max ? [min] : [max, (min + max) / 2, min];
  const start = useTime ? new Date(times[0]).toLocaleDateString() : "First attempt";
  const end = useTime ? new Date(times.at(-1)!).toLocaleDateString() : "Latest";
  return <>
    <svg className="loss-curve" viewBox="0 0 644 190" role="img" aria-label={`${job.metric_cmd} loss curve. Best ${number.format(best.at(-1)!)}.`}>
      {ticks.map((tick) => <g key={tick}><line x1="62" x2="610" y1={y(tick)} y2={y(tick)} className="chart-grid" />
        <text x="57" y={y(tick) + 4} textAnchor="end">{number.format(tick)}</text></g>)}
      {job.target !== null && <line className="chart-target" x1="62" x2="610" y1={y(job.target)} y2={y(job.target)} />}
      <polyline className="chart-attempts" points={measured.map((item, index) => `${x(index)},${y(item.value)}`).join(" ")} />
      <polyline className="chart-best" points={best.map((item, index) => `${x(index)},${y(item)}`).join(" ")} />
      {measured.map((item, index) => <circle key={item.attempt_id} cx={x(index)} cy={y(item.value)} r="3"><title>{`#${item.n} · ${number.format(item.value)}`}</title></circle>)}
      <text x="62" y="180">{start}</text><text x="610" y="180" textAnchor="end">{end}</text>
    </svg>
    <div className="actions meta"><span>Each attempt</span><strong className="best-value">Best {number.format(best.at(-1)!)}</strong>
      {job.target !== null && <span>Target {number.format(job.target)}</span>}</div>
  </>;
}

function JobSeries({ job, revision }: { job: HillclimbJob; revision: number }) {
  const series = useResource(`hillclimb/jobs/${encodeURIComponent(job.id)}/series`, revision, optionalResource<HillclimbSeries>);
  const points = series.data?.points ?? [];
  const current = series.data?.job ?? job;
  return <>
    <div className="heading"><h3>{current.id}</h3><span className="pill">{current.status}</span></div>
    <p className="meta">{current.metric_cmd} · {current.direction === "max" ? "Higher is better" : "Lower is better"}
      {current.stop_reason && ` · ${current.stop_reason}`}</p>
    <ResourceState error={series.error} loading={series.data === undefined} retry={series.refresh} />
    {series.data !== undefined && <LossCurve job={current} attempts={points} />}
    <AttemptList points={points} />
  </>;
}

export function Metrics({ briefID, knownJobs, revision }: { briefID: string; knownJobs?: HillclimbJob[]; revision: number }) {
  const feed = useResource(`hillclimb/jobs?brief_id=${encodeURIComponent(briefID)}`, revision, optionalResource<{ jobs: HillclimbJob[] }>);
  const [selected, setSelected] = useState("");
  const jobs = feed.data?.jobs ?? knownJobs ?? [];
  const job = jobs.find((item) => item.id === selected) ?? jobs[0];
  return <section className="panel stack">
    <div className="heading"><h2>Metric progress</h2>
      {jobs.length > 1 && <label>Job<select value={job.id} onChange={(event) => setSelected(event.target.value)}>
        {jobs.map((item) => <option key={item.id} value={item.id}>{item.id} · {item.metric_cmd}</option>)}
      </select></label>}
    </div>
    <ResourceState error={feed.error} loading={feed.data === undefined && !knownJobs} retry={feed.refresh} />
    {job ? <JobSeries key={job.id} job={job} revision={revision} /> : feed.data !== undefined && <EmptyState>No metric jobs for this brief.</EmptyState>}
  </section>;
}
