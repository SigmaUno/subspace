"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { memo, useContext, useDeferredValue, useMemo, useState } from "react";
import { api, commaList, label, money, safeURL, useResource, useSubmit, value } from "../../../lib/client";
import type { PRStatus } from "../../../lib/pr-types";
import { statuses, type Agent, type Board, type Brief, type Task, type Thread } from "../../../lib/types";
import { ImportBrief } from "./brief-import";
import { BulkBar, BulkToast, QuickMenu, SelectAll, SelectBox, taskActions, useBulk, type Bulk } from "./bulk";
import { Clock, Feedback, Heartbeat, staleAt } from "./components";
import { PRBadges, usePRs } from "./pr-status";
import { EmptyState, ResourceState } from "./states";
import { VirtualList } from "./virtual";

export function BriefsView({ revision }: { revision: number }) {
  const [creating, setCreating] = useState(false);
  const [importing, setImporting] = useState(false);
  const { data, error, refresh } = useResource<{ briefs: Brief[] }>("briefs?limit=1000", revision);
  const form = useSubmit();
  const router = useRouter();

  return (
    <>
      <div className="heading"><h1>Briefs</h1><div className="actions">
        <button onClick={() => setImporting(!importing)}>{importing ? "Close import" : "Import brief"}</button>
        <button className="primary" onClick={() => setCreating(!creating)}>{creating ? "Close form" : "New brief"}</button>
      </div></div>
      {importing && <ImportBrief />}
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
      <ResourceState error={error} loading={!data} retry={refresh} rows={4} />
      {data && !(data.briefs?.length) && <EmptyState>No briefs yet. Create a brief to start a board.</EmptyState>}
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

const TaskCard = memo(function TaskCard({ task, agent, agents, bulk, pr: prStatus, openThread }: {
  task: Task; agent?: Agent; agents: Agent[]; bulk: Bulk; pr?: PRStatus; openThread: (thread: Thread) => void;
}) {
  const pr = safeURL(task.pr_url);
  const now = useContext(Clock);
  const attention = ["blocked", "needs_owner"].includes(task.status) ||
    task.status === "in_progress" && staleAt(task.last_heartbeat_at, now, task.lease_expires_at);
  return (
    <article className={`task ${attention ? "needs-attention" : ""}`}>
      <SelectBox id={task.id} bulk={bulk} name={task.title} />
      <button className="text-button task-title" onClick={() => openThread({ kind: "task", id: task.id, title: task.title })}>{task.title}</button>
      <div className="meta"><Link href={`/tasks/${encodeURIComponent(task.id)}`}>{task.id}</Link> · {task.role}</div>
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
      {prStatus ? <PRBadges pr={prStatus} /> : pr && <a href={pr} target="_blank" rel="noreferrer">Open pull request</a>}
      <QuickMenu taskID={task.id} agents={agents} bulk={bulk} />
    </article>
  );
});

export function Kanban({ board, agents, revision, openThread }: {
  board: Board; agents: Agent[]; revision: number; openThread: (thread: Thread) => void;
}) {
  const [filter, setFilter] = useState("");
  const [statusFilter, setStatus] = useState("");
  const [mergeReady, setMergeReady] = useState(false);
  const deferredFilter = useDeferredValue(filter);
  const prs = usePRs(revision, board.brief.id);
  const bulk = useBulk();
  const byID = useMemo(() => new Map(agents.map((agent) => [agent.id, agent])), [agents]);
  const visibleStatuses = statusFilter ? statuses.filter(([status]) => status === statusFilter) : statuses;
  const columns = useMemo(() => {
    const needle = deferredFilter.toLowerCase().trim();
    return Object.fromEntries(statuses.map(([status]) => [status, (board.columns[status] ?? []).filter((task) =>
      (!needle || [task.title, task.id, task.assignee, task.repo].some((field) => field?.toLowerCase().includes(needle))) &&
      (!mergeReady || !!prs.byTask.get(task.id)?.merge_ready))]));
  }, [board, deferredFilter, mergeReady, prs.byTask]);
  const visible = visibleStatuses.flatMap(([status]) => columns[status]);

  return <section className="stack">
    <div className="heading">
      <h2>Kanban</h2>
      <div className="actions">
        <SelectAll ids={visible.map((task) => task.id)} bulk={bulk} name="tasks" />
        <input className="filter-input" type="search" aria-label="Filter tasks" placeholder="Filter tasks"
          value={filter} onChange={(event) => setFilter(event.target.value)} />
        <select className="filter-input" aria-label="Task status" value={statusFilter} onChange={(event) => setStatus(event.target.value)}>
          <option value="">All statuses</option>
          {statuses.map(([status, name]) => <option key={status} value={status}>{name}</option>)}
        </select>
        {prs.supported && <label className="inline">
          <input type="checkbox" checked={mergeReady} onChange={(event) => setMergeReady(event.target.checked)} /> Merge ready
        </label>}
      </div>
    </div>
    <section className="board" aria-label="Kanban board" tabIndex={0}>
      {visibleStatuses.map(([status, name]) => {
        const tasks = columns[status];
        return <section className="column" aria-label={name} key={status} data-status={status}>
          <h3><span className="pill" data-status={status}>{name}</span><span className="meta">{tasks.length}</span></h3>
          <VirtualList items={tasks} name={`${name} tasks`} estimate={207} className="column-list"
            render={(task) => <TaskCard task={task} agent={byID.get(task.assignee || "")} agents={agents} bulk={bulk} pr={prs.byTask.get(task.id)} openThread={openThread} />} />
        </section>;
      })}
    </section>
    <BulkBar kind="tasks" bulk={bulk} agents={agents} actions={taskActions} />
    <BulkToast bulk={bulk} />
  </section>;
}
