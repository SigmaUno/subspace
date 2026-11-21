"use client";

import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { memo, useContext, useDeferredValue, useEffect, useMemo, useState } from "react";
import { api, commaList, label, money, safeURL, useResource, useSubmit, value } from "../../../lib/client";
import type { PRStatus } from "../../../lib/pr-types";
import type { Component } from "../../../lib/swarm";
import { statuses, type Agent, type Board, type Brief, type Task, type TaskStatus, type Thread } from "../../../lib/types";
import { BoardFilters, useBoardPrefs } from "./board-views";
import { emptyQuery, facets, filterTasks, formatQuery, groupStatuses, haystack, parseQuery } from "../../../lib/board-query";
import { ImportBrief } from "./brief-import";
import { BulkBar, BulkToast, QuickMenu, SelectAll, SelectBox, taskActions, useBulk, type Bulk } from "./bulk";
import { Clock, EntityLink, Feedback, Heartbeat, staleAt } from "./components";
import { PlaybookPicker, usePlaybooks } from "./playbooks";
import { PRBadges, usePRs } from "./pr-status";
import { typing } from "./shortcuts";
import { EmptyState, ResourceState } from "./states";
import { VirtualList } from "./virtual";

export function BriefsView({ revision }: { revision: number }) {
  const [creating, setCreating] = useState(false);
  const [importing, setImporting] = useState(false);
  const { data, error, refresh } = useResource<{ briefs: Brief[] }>("briefs?limit=1000", revision);
  const form = useSubmit();
  const router = useRouter();
  const playbooks = usePlaybooks();
  const [playbook, setPlaybook] = useState("");

  return (
    <>
      <div className="heading"><h1>Briefs</h1><div className="actions">
        <button data-write onClick={() => setImporting(!importing)}>{importing ? "Close import" : "Import brief"}</button>
        <button className="primary" data-write onClick={() => setCreating(!creating)}>{creating ? "Close form" : "New brief"}</button>
      </div></div>
      {importing && <ImportBrief />}
      {creating && (
        <form className="panel stack" data-write onSubmit={(event) => form.submit(event, async (fields) => {
          const brief = await api<Brief>("briefs", "POST", {
            title: value(fields, "title"), body: value(fields, "body"),
            repos: commaList(value(fields, "repos")),
            allowed_providers: commaList(value(fields, "providers")),
            max_cost_eur: Number(value(fields, "cost")),
            max_parallel_agents: Number(value(fields, "parallel")),
            playbook: value(fields, "playbook"),
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
            {!!playbooks.list.length && <PlaybookPicker playbooks={playbooks.list} selected={playbook} onChange={setPlaybook} />}
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
            <span className="meta">{brief.id} · {brief.status}{brief.playbook && ` · ${brief.playbook}`}</span>
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
      <div className="meta"><EntityLink kind="task" id={task.id} /> · {task.role}</div>
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

export function Kanban({ board, agents, components, revision, openThread }: {
  board: Board; agents: Agent[]; components: Component[]; revision: number; openThread: (thread: Thread) => void;
}) {
  const now = useContext(Clock);
  const pathname = usePathname();
  const search = useSearchParams();
  const [query, setQuery] = useState(() => parseQuery(search.toString()));
  const deferredQuery = useDeferredValue(query);
  const { density, setDensity, collapsed, toggleCollapsed } = useBoardPrefs();
  const prs = usePRs(revision, board.brief.id);
  const bulk = useBulk();
  const byID = useMemo(() => new Map(agents.map((agent) => [agent.id, agent])), [agents]);
  const tasks = useMemo(() => statuses.flatMap(([status]) => board.columns[status] ?? []), [board]);
  const haystacks = useMemo(() => new Map(tasks.map((task) => [task.id, haystack(task)])), [tasks]);
  const options = useMemo(() => facets(tasks), [tasks]);
  const matched = useMemo(() => filterTasks(tasks, haystacks, deferredQuery, {
    isStale: (task) => staleAt(task.last_heartbeat_at, now, task.lease_expires_at),
    isMergeReady: (id) => !!prs.byTask.get(id)?.merge_ready,
  }), [tasks, haystacks, deferredQuery, now, prs.byTask]);
  const columns = useMemo(() => {
    const grouped: Partial<Record<TaskStatus, Task[]>> = {};
    for (const task of matched) (grouped[task.status] ??= []).push(task);
    return grouped;
  }, [matched]);
  const groupFilter = groupStatuses(deferredQuery.group);
  const visibleStatuses = groupFilter ? statuses.filter(([status]) => groupFilter.includes(status)) : statuses;
  const visible = visibleStatuses.flatMap(([status]) => columns[status] ?? []);

  useEffect(() => {
    const next = formatQuery(query);
    if (next === window.location.search.slice(1)) return;
    window.history.replaceState(window.history.state, "", next ? `${pathname}?${next}` : pathname);
  }, [query, pathname]);

  useEffect(() => {
    const actions: Record<string, () => void> = {
      B: () => setQuery((current) => ({ ...current, blocked: !current.blocked })),
      S: () => setQuery((current) => ({ ...current, stale: !current.stale })),
      M: () => { if (prs.supported) setQuery((current) => ({ ...current, mergeReady: !current.mergeReady })); },
      D: () => setDensity(density === "compact" ? "comfortable" : "compact"),
      X: () => setQuery(emptyQuery),
    };
    function onKey(event: KeyboardEvent) {
      if (!event.shiftKey || event.ctrlKey || event.metaKey || event.altKey || typing(event.target)) return;
      const action = actions[event.key];
      if (!action) return;
      event.preventDefault();
      action();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [density, prs.supported, setDensity]);

  return <section className="stack">
    <div className="heading">
      <h2>Kanban</h2>
      <div className="actions">
        <SelectAll ids={visible.map((task) => task.id)} bulk={bulk} name="tasks" />
      </div>
    </div>
    <BoardFilters query={query} onChange={setQuery} facets={options} components={components} mergeSupported={prs.supported}
      shown={visible.length} total={tasks.length} density={density} onDensity={setDensity} />
    <section className="board" data-density={density} aria-label="Kanban board" tabIndex={0}>
      {visibleStatuses.map(([status, name]) => {
        const items = columns[status] ?? [];
        const folded = collapsed.has(status);
        return <section className="column" aria-label={name} key={status} data-status={status} data-collapsed={folded}>
          <h3><span className="pill" data-status={status}>{name}</span>
            <span className="column-tools"><span className="meta">{items.length}</span>
              <button type="button" className="column-toggle" aria-expanded={!folded} aria-label={`${folded ? "Show" : "Hide"} ${name} tasks`}
                onClick={() => toggleCollapsed(status)}>{folded ? "+" : "−"}</button></span></h3>
          {!folded && <VirtualList items={items} name={`${name} tasks`} estimate={density === "compact" ? 120 : 207} className="column-list"
            render={(task) => <TaskCard task={task} agent={byID.get(task.assignee || "")} agents={agents} bulk={bulk} pr={prs.byTask.get(task.id)} openThread={openThread} />} />}
        </section>;
      })}
    </section>
    <BulkBar kind="tasks" bulk={bulk} agents={agents} actions={taskActions} />
    <BulkToast bulk={bulk} />
  </section>;
}
