"use client";

import { memo, useEffect, useState, type ReactNode } from "react";
import { label, money, optionalResource, useResource } from "../../../../lib/client";
import type { ActivityItem, ActivityPage } from "../../../../lib/activity-types";
import { forecastMinutes } from "../../../../lib/duration";
import type { Forecast } from "../../../../lib/generated-types";
import { stalledTasks } from "../../../../lib/mission";
import type { PRList } from "../../../../lib/pr-types";
import type { AlertList, BreakerList } from "../../../../lib/settings-types";
import type { TrainList } from "../../../../lib/trains-types";
import type { SpendLimits } from "../../../../lib/usage-types";
import type { Agent, Brief, Task, TaskStatus } from "../../../../lib/types";
import { statuses } from "../../../../lib/types";
import { shortStamp } from "../shared/format";
import { useLive } from "../shared/live";
import { ConnectionStatus } from "../shared/states";
import "./mission.css";

type TaskState = {
  paused: boolean;
  pause_reason: string | null;
  counts: { agents: number; agents_active: number; tasks: Partial<Record<TaskStatus, number>> };
};

const breakerClass = { closed: "active", open: "revoked", half_open: "paused" } as const;

function useWallClock() {
  const [now, setNow] = useState(0);
  useEffect(() => {
    const initial = setTimeout(() => setNow(Date.now()), 0);
    const interval = setInterval(() => setNow(Date.now()), 15_000);
    return () => { clearTimeout(initial); clearInterval(interval); };
  }, []);
  return now;
}

function minutesSince(time: string | null, now: number) {
  if (!time || !now) return "—";
  return `${Math.max(0, Math.floor((now - Date.parse(time)) / 60_000))}m`;
}

function Tile({ title, tone, wide, children }: { title: string; tone?: "warning" | "danger"; wide?: boolean; children: ReactNode }) {
  return <section className={`mission-tile${wide ? " wide" : ""}`} data-tone={tone}>
    <h2>{title}</h2>
    {children}
  </section>;
}

function FullscreenToggle() {
  const [active, setActive] = useState(false);
  useEffect(() => {
    const sync = () => setActive(document.fullscreenElement !== null);
    document.addEventListener("fullscreenchange", sync);
    return () => document.removeEventListener("fullscreenchange", sync);
  }, []);
  function toggle() {
    if (document.fullscreenElement) {
      void document.exitFullscreen();
    } else if (document.fullscreenEnabled) {
      void document.documentElement.requestFullscreen().catch(() => undefined);
    }
  }
  return <button type="button" aria-pressed={active} onClick={toggle}>{active ? "Exit fullscreen" : "Fullscreen"}</button>;
}

const BriefRing = memo(function BriefRing({ brief, revision }: { brief: Brief; revision: number }) {
  const feed = useResource(`briefs/${encodeURIComponent(brief.id)}/forecast`, revision, optionalResource<Forecast>);
  const forecast = feed.data;
  const total = forecast?.tasks.total ?? 0;
  const complete = forecast?.tasks.complete ?? 0;
  const fraction = total ? complete / total : 0;
  const percent = Math.round(fraction * 100);
  const circumference = 2 * Math.PI * 52;
  const eta = forecast === undefined ? (feed.error ? "Forecast unavailable" : "Forecasting…")
    : forecast === null ? "No forecast yet"
      : !total ? "No tasks yet"
        : forecast.tasks.remaining === 0 ? "Complete"
          : `ETA ${shortStamp(forecast.schedule.completes_at.p50)} · ${forecastMinutes(forecast.schedule.remaining_minutes.p50)} left`;

  return <article className="mission-ring-card">
    <svg viewBox="0 0 120 120" role="img" aria-label={`${brief.title}: ${percent}% complete`}>
      <circle className="ring-track" cx="60" cy="60" r="52" />
      <circle className="ring-value" cx="60" cy="60" r="52" transform="rotate(-90 60 60)"
        strokeDasharray={circumference} strokeDashoffset={circumference * (1 - fraction)} />
      <text x="60" y="60" textAnchor="middle" dominantBaseline="central">{percent}%</text>
    </svg>
    <div className="mission-ring-text">
      <h3 className="ellipsis" title={brief.title}>{brief.title}</h3>
      <p>{eta}</p>
      <p className="mission-sub">{money(brief.cost_used)}{brief.budget.max_cost_eur > 0 && ` of ${money(brief.budget.max_cost_eur)}`}</p>
    </div>
  </article>;
});

function TickerRun({ items, hidden = false }: { items: ActivityItem[]; hidden?: boolean }) {
  return <div className="ticker-run" aria-hidden={hidden || undefined}>
    {items.map((item) => <span key={item.id} data-severity={item.severity}>{item.text}</span>)}
  </div>;
}

function Ticker({ items }: { items: ActivityItem[] }) {
  if (!items.length) return <section className="mission-ticker" aria-label="Recent activity"><p>Waiting for activity…</p></section>;
  return <section className="mission-ticker" aria-label="Recent activity">
    <div className="ticker-track" style={{ animationDuration: `${Math.max(30, items.length * 5)}s` }}>
      <TickerRun items={items} />
      <TickerRun items={items} hidden />
    </div>
  </section>;
}

export function MissionPage({ viewer }: { viewer: boolean }) {
  const { revisions, status } = useLive("all");
  const now = useWallClock();
  const control = useResource<TaskState>("state", revisions.control);
  const agents = useResource<{ agents: Agent[] | null }>("agents?limit=1000", revisions.agents);
  const tasks = useResource<{ tasks: Task[] | null }>("tasks?limit=1000", revisions.board);
  const prs = useResource<PRList>("prs?state=open&limit=1000", revisions.board);
  const trains = useResource("mergequeue/trains?limit=1", revisions.board, optionalResource<TrainList>);
  const limits = useResource<SpendLimits>("limits", revisions.board);
  const alerts = useResource<AlertList>("alerts?status=active", revisions.decisions);
  const breakers = useResource<BreakerList>("breakers", revisions.control);
  const briefs = useResource<{ briefs: Brief[] | null }>("briefs?status=open&limit=6", revisions.briefs);
  const activity = useResource<ActivityPage>("activity?limit=50", revisions.activity);

  const agentList = agents.data?.agents ?? [];
  const activeAgents = agentList.filter((agent) => agent.status === "active").length;
  const pausedAgents = agentList.filter((agent) => agent.status === "paused").length;
  const taskCounts: Partial<Record<TaskStatus, number>> = control.data?.counts.tasks ?? {};
  const taskTotal = Object.values(taskCounts).reduce((sum, count) => sum + (count ?? 0), 0);
  const openPRs = prs.data?.prs ?? [];
  const mergeReady = openPRs.filter((pr) => pr.merge_ready).length;
  const failingPRs = openPRs.filter((pr) => pr.checks === "failure").length;
  const train = trains.data?.trains?.[0];
  const daily = limits.data?.windows.find((window) => window.window === "daily");
  const activeAlerts = alerts.data?.alerts ?? [];
  const criticalAlert = activeAlerts.some((alert) => alert.severity === "critical");
  const breakerList = breakers.data?.breakers ?? [];
  const stalled = stalledTasks(tasks.data?.tasks ?? [], now);

  return <div className="mission" data-viewer={viewer || undefined}>
    <div className="mission-drift">
      <header className="mission-head">
        <div>
          <p className="mission-kicker">Subspace</p>
          <h1>Mission control</h1>
        </div>
        <div className="mission-head-end">
          {control.data?.paused && <span className="pill revoked">Paused{control.data.pause_reason ? ` · ${control.data.pause_reason}` : ""}</span>}
          <ConnectionStatus status={status} />
          <time className="mission-clock">{now ? new Date(now).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : ""}</time>
          {!viewer && <FullscreenToggle />}
        </div>
      </header>

      <div className="mission-tiles">
        <Tile title="Agents" tone={pausedAgents ? "warning" : undefined}>
          <strong>{agents.data ? activeAgents : "—"}<small> active</small></strong>
          <span className="mission-sub">{control.data?.paused ? "Swarm paused" : `${pausedAgents} paused`}</span>
        </Tile>
        <Tile title="Tasks by status" wide>
          <div className="mission-bar" role="img" aria-label={`${taskTotal} tasks`}>
            {statuses.filter(([key]) => (taskCounts[key] ?? 0) > 0).map(([key, name]) => <span key={key} title={`${name}: ${taskCounts[key]}`}
              style={{ flexGrow: taskCounts[key], background: `var(--s-${key}-fg)` }} />)}
          </div>
          <ul className="mission-legend">
            {statuses.filter(([key]) => (taskCounts[key] ?? 0) > 0).map(([key, name]) => <li key={key}>
              <i style={{ background: `var(--s-${key}-fg)` }} aria-hidden="true" />{name} <b>{taskCounts[key]}</b>
            </li>)}
          </ul>
        </Tile>
        <Tile title="PRs in flight">
          <strong>{prs.data ? openPRs.length : "—"}</strong>
          <span className="mission-sub">{mergeReady} merge-ready · {failingPRs} failing</span>
        </Tile>
        <Tile title="Merge train" tone={train?.state === "failed" ? "danger" : undefined}>
          <strong className="mission-word">{trains.data === undefined ? "—" : train?.state ?? "idle"}</strong>
          <span className="mission-sub ellipsis">{train ? `${train.repo} · ${train.members?.length ?? 0} PRs` : "No train queued"}</span>
        </Tile>
        <Tile title="Spend today" tone={daily?.level === "exceeded" ? "danger" : daily?.level === "warning" ? "warning" : undefined}>
          <strong>{daily ? money(daily.spent_eur) : "—"}</strong>
          {daily && daily.cap_eur > 0
            ? <>
              <progress max={daily.cap_eur} value={Math.min(daily.spent_eur, daily.cap_eur)} aria-label="Daily spend against cap" />
              <span className="mission-sub">of {money(daily.cap_eur)} cap</span>
            </>
            : <span className="mission-sub">no daily cap</span>}
        </Tile>
        <Tile title="Alerts" tone={activeAlerts.length ? (criticalAlert ? "danger" : "warning") : undefined}>
          <strong>{alerts.data ? activeAlerts.length : "—"}</strong>
          <span className="mission-sub ellipsis">{activeAlerts[0]?.title ?? "All clear"}</span>
        </Tile>
      </div>

      <div className="mission-main">
        <section className="mission-panel mission-briefs" aria-labelledby="mission-briefs-title">
          <h2 id="mission-briefs-title">Briefs</h2>
          {briefs.data && !(briefs.data.briefs ?? []).length && <p className="mission-empty">No open briefs</p>}
          <div className="mission-rings">
            {(briefs.data?.briefs ?? []).map((brief) => <BriefRing key={brief.id} brief={brief} revision={revisions.board} />)}
          </div>
        </section>

        <section className="mission-panel" aria-labelledby="mission-stalled-title">
          <h2 id="mission-stalled-title">Stalled tasks</h2>
          {!tasks.data && <p className="mission-empty">Loading…</p>}
          {tasks.data && !stalled.length && <p className="mission-empty">Nothing stalled</p>}
          {!!stalled.length && <ol className="mission-list">
            {stalled.map((task) => <li key={task.id}>
              <div className="mission-row-head">
                <span className="mission-id">{task.id}</span>
                <span className="pill" data-status={task.status}>{label(task.status)}</span>
              </div>
              <span className="ellipsis" title={task.title}>{task.title}</span>
              <span className="mission-sub ellipsis">
                {task.status === "in_progress" ? `silent ${minutesSince(task.last_heartbeat_at, now)}` : task.blocked_reason || task.assignee || "unassigned"}
              </span>
            </li>)}
          </ol>}
        </section>

        <section className="mission-panel" aria-labelledby="mission-breakers-title">
          <h2 id="mission-breakers-title">Provider breakers</h2>
          {!breakers.data && <p className="mission-empty">Loading…</p>}
          {breakers.data && !breakerList.length && <p className="mission-empty">No provider samples yet</p>}
          {!!breakerList.length && <ul className="mission-list">
            {breakerList.map((breaker) => <li key={`${breaker.provider}:${breaker.model}`}>
              <div className="mission-row-head">
                <span>{breaker.provider}</span>
                <span className={`pill ${breakerClass[breaker.state]}`}>{breaker.state.replace("_", " ")}</span>
              </div>
              <span className="mission-sub ellipsis">{breaker.model} · {Math.round(breaker.error_rate * 100)}% errors</span>
            </li>)}
          </ul>}
        </section>
      </div>

      <Ticker items={activity.data?.items ?? []} />
    </div>
  </div>;
}
