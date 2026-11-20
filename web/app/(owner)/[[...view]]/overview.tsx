"use client";

import Link from "next/link";
import { useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { api, label, money, useResource } from "../../../lib/client";
import type { AuditEvent, BriefProgress, DecisionInbox, SwarmBoards, SwarmState } from "../../../lib/overview-types";
import { statuses, type Agent, type Board, type Brief, type Capacity } from "../../../lib/types";
import { Clock, Feedback } from "./components";
import { useLive } from "./live";
import { Workspace } from "./workspace";

const staleAfter = 5 * 60_000;
const feedSize = 50;
const auditPage = 1000;

async function loadBoards(path: string, signal: AbortSignal): Promise<SwarmBoards> {
  const { briefs } = await api<{ briefs: Brief[] | null }>(path, "GET", undefined, signal);
  const boards = await Promise.all((briefs ?? []).map((brief) =>
    api<Board>(`briefs/${encodeURIComponent(brief.id)}/board`, "GET", undefined, signal)));
  const titles: Record<string, string> = {};
  const progress = boards.map((board): BriefProgress => {
    const counts: BriefProgress["counts"] = {};
    let total = 0;
    for (const [status] of statuses) {
      const tasks = board.columns[status] ?? [];
      counts[status] = tasks.length;
      total += tasks.length;
      tasks.forEach((task) => { titles[task.id] = task.title; });
    }
    return { brief: board.brief, counts, total };
  });
  return { briefs: progress, titles };
}

function useAudit(revision: number) {
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [error, setError] = useState("");
  const cursor = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    (async () => {
      for (;;) {
        const { events: page } = await api<{ events: AuditEvent[] | null }>(
          `audit?since=${cursor.current}&limit=${auditPage}`, "GET", undefined, controller.signal);
        if (controller.signal.aborted) return;
        const batch = page ?? [];
        if (batch.length) {
          cursor.current = batch[batch.length - 1].id;
          setEvents((current) => [...current, ...batch].slice(-feedSize));
        }
        setError("");
        if (batch.length < auditPage) return;
      }
    })().catch((cause: Error) => { if (!controller.signal.aborted) setError(cause.message); });
    return () => controller.abort();
  }, [revision]);

  return { events, error };
}

function age(milliseconds: number) {
  const seconds = Math.max(0, Math.floor(milliseconds / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return minutes < 60 ? `${minutes}m` : `${Math.floor(minutes / 60)}h`;
}

function Stat({ name, children }: { name: string; children: ReactNode }) {
  return <div className="panel stat"><span className="meta">{name}</span>{children}</div>;
}

function BriefRow({ progress }: { progress: BriefProgress }) {
  const { brief, counts, total } = progress;
  const scope = total - (counts.cancelled ?? 0);
  const done = counts.done ?? 0;
  const review = (counts.submitted ?? 0) + (counts.in_review ?? 0) + (counts.changes_requested ?? 0);
  const stuck = (counts.blocked ?? 0) + (counts.needs_owner ?? 0);
  return (
    <Link href={`/briefs/${encodeURIComponent(brief.id)}`} className="panel brief-link">
      <div className="heading"><h2>{brief.title}</h2><span className="meta">{brief.id}</span></div>
      <div className="progress">
        <progress aria-label={`${brief.title} progress`} value={done} max={Math.max(scope, 1)} />
        <span>{done}/{scope}</span>
      </div>
      <span className="meta">
        {counts.in_progress ?? 0} in progress · {review} in review · {counts.ready ?? 0} ready
        {stuck > 0 && <span className="stale"> · {stuck} blocked</span>}
        {" · "}{money(brief.cost_used)} / {money(brief.budget.max_cost_eur)}
      </span>
    </Link>
  );
}

function AgentCard({ agent, titles }: { agent: Agent; titles: Record<string, string> }) {
  const now = useContext(Clock);
  const seen = agent.last_seen_at ? Date.parse(agent.last_seen_at) : null;
  const watch = agent.status === "active";
  const stale = watch && (seen === null ? !!agent.current_task_id : now - seen > staleAfter);
  const task = agent.current_task_id;
  return (
    <article className={`panel agent-card ${stale ? "stale-agent" : ""}`}>
      <div className="heading"><strong className="break">{agent.id}</strong><span className={`pill ${agent.status}`}>{agent.status}</span></div>
      <span className="meta break">{agent.role} · {agent.kind} · {agent.model}</span>
      <span className={stale ? "stale" : "meta"}>
        {seen === null || !now ? "No heartbeat" : `Heartbeat ${age(now - seen)} ago`}{stale && " · stale"}
      </span>
      <span className="break">{task ? <>{task} <span className="muted">{titles[task]}</span></> : <span className="muted">Idle</span>}</span>
      {agent.pause_reason && <span className="stale">{label(agent.pause_reason)}</span>}
    </article>
  );
}

function FeedItem({ event, titles }: { event: AuditEvent; titles: Record<string, string> }) {
  const from = event.before?.status;
  const to = event.after?.status;
  return (
    <li>
      <time className="meta" dateTime={event.ts}>{new Date(event.ts).toLocaleTimeString([], { timeStyle: "medium" })}</time>
      <span className="break">
        <strong>{event.agent_id}</strong> {label(event.tool)}
        {event.task_id && <> <span className="muted">{event.task_id}{titles[event.task_id] && ` ${titles[event.task_id]}`}</span></>}
        {to && <span className="meta"> {from ? `${label(from)} → ` : "→ "}{label(to)}</span>}
      </span>
    </li>
  );
}

export function Overview() {
  const [now, setNow] = useState(0);
  useEffect(() => {
    const initial = setTimeout(() => setNow(Date.now()), 0);
    const interval = setInterval(() => setNow(Date.now()), 5_000);
    return () => { clearTimeout(initial); clearInterval(interval); };
  }, []);

  const { revisions } = useLive("board,agents,control,questions,plans");
  const activity = revisions.board + revisions.agents + revisions.control + revisions.questions + revisions.plans;
  const state = useResource<SwarmState>("state", revisions.control);
  const capacity = useResource<Capacity>("capacity", activity);
  const boards = useResource<SwarmBoards>("briefs?status=open&limit=1000", activity, loadBoards);
  const agents = useResource<{ agents: Agent[] | null }>("agents?limit=1000", revisions.agents);
  const inbox = useResource<DecisionInbox>("decisions?limit=1000", activity);
  const audit = useAudit(activity);
  const [resuming, setResuming] = useState(false);
  const [resumeError, setResumeError] = useState("");

  async function resume() {
    setResuming(true);
    setResumeError("");
    try {
      await api<{ paused: boolean }>("resume-all", "POST", {});
      state.refresh();
    } catch (cause) {
      setResumeError(cause instanceof Error ? cause.message : "Resume failed.");
    } finally {
      setResuming(false);
    }
  }

  const counts = state.data?.counts;
  const titles = boards.data?.titles ?? {};
  const roster = agents.data?.agents?.filter((agent) => agent.status !== "revoked") ?? [];
  const limits = capacity.data?.limits ?? [];

  return (
    <div className="overview-root">
      <Workspace section="overview" />
      <Clock.Provider value={now}>
        <main className="workspace overview">
          <div className="heading"><h1>Swarm overview</h1></div>
          <Feedback error={state.error || capacity.error || boards.error || agents.error || inbox.error || audit.error || resumeError} />
          {state.data?.paused && (
            <div className="notice paused-banner" role="status">
              <span>All agents paused{state.data.pause_reason && ` · ${state.data.pause_reason}`}</span>
              <button className="primary" onClick={resume} disabled={resuming}>Resume all</button>
            </div>
          )}
          <section className="stat-grid" aria-label="Swarm status">
            <Stat name="Agents active">
              <strong>{counts?.agents_active ?? "—"}<span className="muted"> / {counts?.agents ?? "—"}</span></strong>
            </Stat>
            <Stat name="In use / capacity">
              <strong>{capacity.data?.agents_in_use ?? "—"}<span className="muted"> / {capacity.data?.global_max_agents ?? "—"}</span></strong>
              {capacity.data && <progress aria-label="Agents in use" value={capacity.data.agents_in_use} max={Math.max(capacity.data.global_max_agents, 1)} />}
              {limits.map((limit) => (
                <span className="meta" key={`${limit.provider}/${limit.model}`}>{limit.provider}/{limit.model} · {limit.in_use} / {limit.max_concurrency}</span>
              ))}
            </Stat>
            <Stat name="Tasks in progress">
              <strong>{counts ? counts.tasks?.in_progress ?? 0 : "—"}</strong>
            </Stat>
            <Link href="/decisions" className="panel stat stat-link">
              <span className="meta">Decision inbox</span>
              <strong className={inbox.data?.decisions?.length ? "stale" : ""}>{inbox.data ? inbox.data.decisions?.length ?? 0 : "—"}</strong>
            </Link>
          </section>
          <div className="overview-layout">
            <div className="stack">
              <section className="stack" aria-label="Brief progress">
                <h2>Briefs</h2>
                <Feedback loading={!boards.data} />
                {boards.data && !boards.data.briefs.length && <p className="empty">No open briefs.</p>}
                <div className="brief-list">{boards.data?.briefs.map((progress) => <BriefRow key={progress.brief.id} progress={progress} />)}</div>
              </section>
              <section className="stack" aria-label="Agents">
                <h2>Agents</h2>
                <Feedback loading={!agents.data} />
                {agents.data && !roster.length && <p className="empty">No agents registered.</p>}
                <div className="agent-grid">{roster.map((agent) => <AgentCard key={agent.id} agent={agent} titles={titles} />)}</div>
              </section>
            </div>
            <section className="panel stack feed-panel" aria-label="Activity">
              <h2>Activity</h2>
              {!audit.events.length && <p className="muted">No activity yet.</p>}
              <ol className="feed">
                {[...audit.events].reverse().map((event) => <FeedItem key={event.id} event={event} titles={titles} />)}
              </ol>
            </section>
          </div>
        </main>
      </Clock.Provider>
    </div>
  );
}
