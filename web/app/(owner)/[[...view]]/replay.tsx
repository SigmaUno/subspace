"use client";

import "./replay.css";
import Link from "next/link";
import { useEffect, useState, type MouseEvent } from "react";
import { label, money, useResource } from "../../../lib/client";
import type { ReplayKeyEvent, ReplayPoint, ReplaySnapshot, ReplayTask, ReplayTimeline } from "../../../lib/replay-types";
import { statuses } from "../../../lib/types";
import { useLive } from "./live";
import { PageShell } from "./shell";
import { stamp } from "./format";
import { EmptyState, ResourceState } from "./states";

const speeds = [1, 2, 5, 10, 25];
const strip = { width: 1000, height: 72, keys: 10 };


function same(a: ReplayTask, b: ReplayTask) {
  return a.status === b.status && a.assignee === b.assignee && a.reviewer === b.reviewer &&
    a.progress_pct === b.progress_pct && a.cost_used === b.cost_used;
}

function changedTasks(current?: ReplaySnapshot, previous?: ReplaySnapshot) {
  const out = new Set<string>();
  if (!current || !previous) return out;
  const before = new Map(Object.values(previous.columns).flatMap((tasks) => tasks ?? []).map((task) => [task.id, task]));
  for (const task of Object.values(current.columns).flatMap((tasks) => tasks ?? [])) {
    const old = before.get(task.id);
    if (!old || !same(old, task)) out.add(task.id);
  }
  return out;
}

function nearest(points: ReplayPoint[], auditID: number) {
  const index = points.findIndex((point) => point.audit_id >= auditID);
  return index < 0 ? points.length - 1 : index;
}

function stack(counts: Record<string, number> | null, high: number, area: number) {
  const out: { status: string; y: number; height: number }[] = [];
  let y = strip.height;
  for (const [status] of statuses) {
    const n = counts?.[status] ?? 0;
    if (!n) continue;
    const height = (n / high) * area;
    y -= height;
    out.push({ status, y, height });
  }
  return out;
}

function Strip({ points, keys, index, seek }: { points: ReplayPoint[]; keys: ReplayKeyEvent[]; index: number; seek: (index: number) => void }) {
  const width = strip.width / Math.max(points.length, 1);
  const high = Math.max(1, ...points.map((point) => Object.values(point.counts ?? {}).reduce((sum, n) => sum + n, 0)));
  const area = strip.height - strip.keys;
  function click(event: MouseEvent<SVGSVGElement>) {
    const box = event.currentTarget.getBoundingClientRect();
    seek(Math.min(points.length - 1, Math.max(0, Math.floor(((event.clientX - box.left) / box.width) * points.length))));
  }
  return <svg className="replay-strip" viewBox={`0 0 ${strip.width} ${strip.height}`} preserveAspectRatio="none" onClick={click} aria-hidden="true">
    {points.map((point, i) => <g key={point.audit_id}>
      {stack(point.counts, high, area).map((bar) =>
        <rect key={bar.status} data-status={bar.status} x={i * width} y={bar.y} width={Math.max(width, 1)} height={bar.height} />)}
    </g>)}
    {keys.map((key) => <rect key={`${key.audit_id}-${key.task_id}`} className="replay-key" x={nearest(points, key.audit_id) * width} y={0} width={Math.max(width, 2)} height={strip.keys - 2} />)}
    <line className="replay-cursor" x1={(index + 0.5) * width} x2={(index + 0.5) * width} y1={0} y2={strip.height} />
  </svg>;
}

function Card({ task, changed }: { task: ReplayTask; changed: boolean }) {
  return <article className={`task ${changed ? "replay-changed" : ""}`} aria-label={`${task.id} ${task.title}${changed ? " (changed)" : ""}`}>
    <span className="meta">{task.id} · {task.role}</span>
    <Link className="task-title" href={`/tasks/${encodeURIComponent(task.id)}`}>{task.title}</Link>
    <span className="meta">{task.assignee ?? "unassigned"}{task.reviewer && ` · review ${task.reviewer}`}</span>
    <span className="progress"><progress max={100} value={task.progress_pct} aria-label={`${task.id} progress`} />{task.progress_pct}%</span>
    <span className="meta">{money(task.cost_used)}{task.changed_at && <> · <time dateTime={task.changed_at}>{stamp(task.changed_at)}</time></>}</span>
  </article>;
}

function describe(key: ReplayKeyEvent) {
  const move = key.to ? `${label(key.from || "new")} → ${label(key.to)}` : label(key.tool);
  return `${key.task_id ? key.task_id + " " : ""}${move}${key.agent_id ? ` · ${key.agent_id}` : ""}`;
}

export function ReplayPage({ id }: { id: string }) {
  const { revisions } = useLive("all");
  const base = `briefs/${encodeURIComponent(id)}/replay`;
  const timeline = useResource<ReplayTimeline>(`${base}/timeline`, revisions.board);
  const points = timeline.data?.points ?? [];
  const keys = timeline.data?.key_events ?? [];
  const last = points.length - 1;
  const [picked, setPicked] = useState<number | null>(null);
  const [playing, setPlaying] = useState(false);
  const [speed, setSpeed] = useState(speeds[1]);
  const index = picked === null ? last : Math.min(picked, last);
  const point = index >= 0 ? points[index] : undefined;
  const snapshot = useResource<ReplaySnapshot>(point ? `${base}?at=${point.audit_id}` : null, revisions.board);

  const [shown, setShown] = useState<{ current?: ReplaySnapshot; previous?: ReplaySnapshot }>({});
  if (snapshot.data && snapshot.data !== shown.current) setShown({ current: snapshot.data, previous: shown.current });
  if (playing && index >= last) setPlaying(false);

  useEffect(() => {
    if (!playing) return;
    const timer = window.setInterval(() => setPicked((current) => (current ?? 0) + 1), 1000 / speed);
    return () => window.clearInterval(timer);
  }, [playing, speed]);

  const view = shown.current;
  const changed = changedTasks(view, shown.previous);
  const seek = (next: number) => { setPlaying(false); setPicked(Math.max(0, Math.min(last, next))); };
  const toggle = () => {
    if (!playing && index >= last) setPicked(0);
    setPlaying(!playing);
  };
  const briefPath = `/briefs/${encodeURIComponent(id)}`;
  const recent = keys.filter((key) => point && key.audit_id <= point.audit_id).slice(-30).reverse();

  return <PageShell section="briefs">
    <div className="heading">
      <div>
        <div className="meta"><Link href={briefPath}>{id}</Link> · replay</div>
        <h1>Replay</h1>
      </div>
      <div className="actions">
        <Link href={`${briefPath}/report`}>Report</Link>
      </div>
    </div>
    <ResourceState error={timeline.error || snapshot.error} loading={!timeline.data} retry={() => { timeline.refresh(); snapshot.refresh(); }} rows={4} />
    {timeline.data && !points.length && <EmptyState>This brief has no recorded events yet.</EmptyState>}
    {points.length > 0 && <section className="panel stack">
      <Strip points={points} keys={keys} index={index} seek={seek} />
      <div className="replay-controls">
        <button type="button" onClick={() => seek(index - 1)} disabled={index <= 0} aria-label="Previous step">◀</button>
        <button type="button" className="primary" onClick={toggle} aria-pressed={playing}>{playing ? "Pause" : "Play"}</button>
        <button type="button" onClick={() => seek(index + 1)} disabled={index >= last} aria-label="Next step">▶</button>
        <input type="range" min={0} max={last} value={index} aria-label="Replay position"
          aria-valuetext={point ? `audit ${point.audit_id}, ${stamp(point.at)}` : undefined}
          onChange={(event) => seek(Number(event.target.value))} />
        <select className="filter-input" aria-label="Playback speed" value={speed} onChange={(event) => setSpeed(Number(event.target.value))}>
          {speeds.map((n) => <option key={n} value={n}>{n} steps/s</option>)}
        </select>
        <button type="button" onClick={() => { setPlaying(false); setPicked(null); }} disabled={picked === null}>Live</button>
      </div>
      <div className="meta">
        Step {index + 1} of {points.length} · audit {point?.audit_id} · <time dateTime={point?.at}>{stamp(point?.at)}</time>
        {(timeline.data?.events ?? 0) > points.length && ` · ${timeline.data?.events} events sampled`}
      </div>
    </section>}

    {view && <>
      <div className="actions">
        {statuses.filter(([status]) => view.counts?.[status]).map(([status, name]) =>
          <span key={status} className="pill" data-status={status}>{name} {view.counts?.[status]}</span>)}
        {changed.size > 0 && <span className="meta">{changed.size} changed since the previous frame</span>}
      </div>
      <section className="board" aria-label="Board at the replay position" tabIndex={0}>
        {statuses.map(([status, name]) => {
          const tasks = view.columns[status] ?? [];
          return <section className="column stack" aria-label={name} key={status} data-status={status}>
            <h3><span className="pill" data-status={status}>{name}</span><span className="meta">{tasks.length}</span></h3>
            {tasks.map((task) => <Card key={task.id} task={task} changed={changed.has(task.id)} />)}
          </section>;
        })}
      </section>
      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(22rem, 1fr))", gap: ".8rem" }}>
        <section className="panel stack">
          <h2>Agents</h2>
          {view.agents?.length ? <ul className="replay-events">
            {view.agents.map((agent) => <li key={agent.id}>
              <strong>{agent.id}</strong> <span className="meta">{agent.role || "agent"} · {agent.events} events</span>
              <div className="meta">
                {agent.assigned?.length ? `Working on ${agent.assigned.join(", ")}` : "No open tasks"}
                {agent.reviewing?.length ? ` · reviewing ${agent.reviewing.join(", ")}` : ""}
              </div>
            </li>)}
          </ul> : <EmptyState>No agents involved yet.</EmptyState>}
        </section>
        <section className="panel stack">
          <h2>Key events</h2>
          {recent.length ? <ul className="replay-events">
            {recent.map((key) => <li key={`${key.audit_id}-${key.task_id}`}>
              <button type="button" onClick={() => seek(nearest(points, key.audit_id))} aria-current={point?.audit_id === key.audit_id ? "step" : undefined}>
                <time dateTime={key.at}>{stamp(key.at)}</time> · {describe(key)}
              </button>
            </li>)}
          </ul> : <EmptyState>No key events up to this point.</EmptyState>}
        </section>
      </div>
    </>}
  </PageShell>;
}
