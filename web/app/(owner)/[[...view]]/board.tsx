"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { api, commaList, label, money, safeURL, useResource, useSubmit, value } from "../../../lib/client";
import { statuses, type Agent, type Board, type Brief, type Task, type Thread } from "../../../lib/types";
import { Feedback, Heartbeat } from "./components";

export function BriefsView({ revision }: { revision: number }) {
  const [creating, setCreating] = useState(false);
  const { data, error } = useResource<{ briefs: Brief[] }>("briefs?limit=1000", revision);
  const form = useSubmit();
  const router = useRouter();

  return (
    <>
      <div className="heading"><h1>Briefs</h1><button className="primary" onClick={() => setCreating(!creating)}>{creating ? "Close form" : "New brief"}</button></div>
      {creating && (
        <form className="panel stack" onSubmit={(event) => form.submit(event, async (fields) => {
          const brief = await api<Brief>("briefs", "POST", {
            title: value(fields, "title"), body: value(fields, "body"),
            repos: commaList(value(fields, "repos")),
            allowed_providers: commaList(value(fields, "providers")),
            max_cost_eur: Number(value(fields, "cost")),
            max_parallel_agents: Number(value(fields, "parallel")),
          });
          router.push(`/briefs/${encodeURIComponent(brief.id)}`);
        })}>
          <h2>Create brief</h2>
          <label>Title<input name="title" required /></label>
          <label>Brief<textarea name="body" rows={5} required /></label>
          <div className="form-grid">
            <label>Repositories<input name="repos" placeholder="owner/repo, owner/another" /></label>
            <label>Allowed providers<input name="providers" placeholder="All providers" /></label>
            <label>Budget (EUR)<input name="cost" type="number" min="0" step="0.01" defaultValue="50" required /></label>
            <label>Max parallel agents<input name="parallel" type="number" min="1" defaultValue="10" required /></label>
          </div>
          <Feedback error={form.error} />
          <div><button type="submit" className="primary" disabled={form.busy}>Create brief</button></div>
        </form>
      )}
      <Feedback error={error} loading={!data} />
      {data && !(data.briefs?.length) && <p className="empty">No briefs yet. Create a brief to start a board.</p>}
      <div className="brief-list">
        {data?.briefs?.map((brief) => (
          <Link key={brief.id} href={`/briefs/${encodeURIComponent(brief.id)}`} className="panel brief-link">
            <span className="meta">{brief.id} · {brief.status}</span>
            <h2>{brief.title}</h2>
            <span className="meta">{brief.repos?.join(", ") || "No repositories"} · Plan {brief.plan_status}</span>
            <span className="meta">{money(brief.cost_used)} / {money(brief.budget.max_cost_eur)} · {brief.budget.max_parallel_agents} parallel agents</span>
          </Link>
        ))}
      </div>
    </>
  );
}

function TaskCard({ task, agent, openThread }: { task: Task; agent?: Agent; openThread: (thread: Thread) => void }) {
  const pr = safeURL(task.pr_url);
  return (
    <article className="task">
      <button className="text-button task-title" onClick={() => openThread({ kind: "task", id: task.id, title: task.title })}>{task.title}</button>
      <div className="meta">{task.id} · {task.role}</div>
      <div>
        {task.assignee
          ? <button className="text-button" onClick={() => openThread({ kind: "agent", id: task.assignee! })}>{task.assignee}</button>
          : <span className="meta">Unassigned</span>}
        {agent && <div className="meta break">{agent.kind} · {agent.model}</div>}
      </div>
      <div className="progress"><progress aria-label="Task progress" value={task.progress_pct} max={100} /><span>{task.progress_pct}%</span></div>
      <Heartbeat time={task.last_heartbeat_at} watch={task.status === "in_progress"} expires={task.lease_expires_at} />
      <div className="meta">{money(task.cost_used)} / {money(task.budget.max_cost_eur)} · {task.minutes_used} / {task.budget.max_minutes} min</div>
      {task.blocked_reason && <span className="stale">{label(task.blocked_reason)}</span>}
      {pr && <a href={pr} target="_blank" rel="noreferrer">Open pull request</a>}
    </article>
  );
}

export function BoardView({ id, revision, agentsRevision, openThread }: {
  id: string; revision: number; agentsRevision: number; openThread: (thread: Thread) => void;
}) {
  const { data, error } = useResource<Board>(`briefs/${encodeURIComponent(id)}/board`, revision);
  const agents = useResource<{ agents: Agent[] }>("agents?limit=1000", agentsRevision);
  const byID = new Map(agents.data?.agents?.map((agent) => [agent.id, agent]));
  return (
    <>
      <Link href="/briefs" className="back">All briefs</Link>
      <Feedback error={error || agents.error} loading={!data} />
      {data && (
        <>
          <div className="heading">
            <div><div className="meta">{id} · {data.brief.status} · plan {data.brief.plan_status}</div><h1>{data.brief.title}</h1></div>
            <button onClick={() => openThread({ kind: "brief", id, title: data.brief.title })}>Brief thread</button>
          </div>
          <details className="panel brief-details"><summary>Brief details</summary>
            <p className="preserve">{data.brief.body}</p>
            <p className="meta">{data.brief.repos?.join(", ")} · {money(data.brief.cost_used)} / {money(data.brief.budget.max_cost_eur)} · {data.brief.budget.max_parallel_agents} parallel agents</p>
          </details>
          <section className="board" aria-label="Kanban board" tabIndex={0}>
            {statuses.map(([status, name]) => {
              const tasks = data.columns[status] ?? [];
              return <section className="column" aria-label={name} key={status}>
                <h2><span>{name}</span><span className="meta">{tasks.length}</span></h2>
                {tasks.map((task) => <TaskCard key={task.id} task={task} agent={byID.get(task.assignee || "")} openThread={openThread} />)}
              </section>;
            })}
          </section>
        </>
      )}
    </>
  );
}
