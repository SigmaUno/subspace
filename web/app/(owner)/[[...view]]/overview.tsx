"use client";

import Link from "next/link";
import { memo, useContext, useDeferredValue, useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { api, label, money, optionalResource, useResource } from "../../../lib/client";
import type { Agent, Board, Brief, Capacity, Message, Task, Thread } from "../../../lib/types";
import type { Component } from "../../../lib/swarm";
import { ActivityFeed } from "./activity-feed";
import { Architecture } from "./architecture";
import { AutopilotPanel } from "./autopilot";
import { Kanban } from "./board";
import { OwnerChat } from "./chat";
import { Clock, Feedback, Heartbeat, staleAt } from "./components";
import { ForecastPanel } from "./forecast";
import { IncidentButton } from "./incident";
import type { Revisions } from "./live";
import { Metrics } from "./metrics";
import { VirtualList } from "./virtual";

const emptyComponents: Component[] = [];
const emptyAgents: Agent[] = [];

function elapsed(minutes: number) {
  const rounded = Math.floor(minutes);
  const hours = Math.floor(rounded / 60);
  return hours ? `${hours}h ${rounded % 60}m` : `${rounded}m`;
}

async function loadLatest(path: string, signal: AbortSignal) {
  let latest: Message | undefined;
  for (;;) {
    const page = await api<{ messages: Message[] }>(`${path}?limit=1000&since=${latest?.id ?? 0}`, "GET", undefined, signal);
    const messages = page.messages ?? [];
    latest = messages.at(-1) ?? latest;
    if (messages.length < 1000) return latest ?? null;
  }
}

function latestMessage(messages: (Message | undefined | null)[]) {
  return messages.reduce<Message | undefined>((newest, current) => current && (!newest || current.id > newest.id) ? current : newest, undefined);
}

const AgentRow = memo(function AgentRow({ agent, task, agentMessage, taskMessage, openThread, openChat }: {
  agent: Agent; task?: Task; agentMessage?: Message; taskMessage?: Message;
  openThread: (thread: Thread) => void; openChat: (target: string) => void;
}) {
  const now = useContext(Clock);
  const live = latestMessage([agent.last_message, agentMessage, taskMessage]);
  const fallback = useResource(agent.last_message === undefined
    ? `threads/agent/${encodeURIComponent(agent.id)}/messages` : null, 0, loadLatest);
  const last = latestMessage([live, fallback.data]);
  const heartbeat = task?.last_heartbeat_at ?? agent.last_seen_at;
  const stale = agent.status === "active" && staleAt(heartbeat, now, task?.lease_expires_at);
  const blocked = task?.status === "blocked" || task?.status === "needs_owner";
  const line = last?.body.split("\n").find((part) => part.trim());
  return <article className={`agent-row ${stale || blocked ? "needs-attention" : ""}`}>
    <div><button className="text-button" type="button" onClick={() => openThread({ kind: "agent", id: agent.id })}>{agent.id}</button>
      <div className="meta ellipsis" title={agent.model}>{agent.kind} · {agent.model}</div></div>
    <div>{task ? <button className="text-button ellipsis" type="button" title={task.title}
      onClick={() => openThread({ kind: "task", id: task.id, title: task.title })}>{task.title}</button> : <span className="meta">No current task</span>}
      <div className="meta">{task
        ? <><span className="pill" data-status={task.status}>{label(task.status)}</span> {task.progress_pct}% · {elapsed(task.minutes_used)}</>
        : `${agent.role} · ${agent.status}`}</div>
    </div>
    <div><Heartbeat time={heartbeat} watch={agent.status === "active"} expires={task?.lease_expires_at} />
      <div className="meta">{agent.assigned_count} assigned</div></div>
    <div className="activity"><p className="ellipsis" title={line}>{line || "No recent messages"}</p>
      <button className="text-button" type="button" onClick={() => openChat(`agent:${agent.id}`)}>Message agent</button></div>
  </article>;
});

function Workload({ agents, tasks, recent, openThread, openChat }: {
  agents: Agent[]; tasks: Task[]; recent: Record<string, Message>;
  openThread: (thread: Thread) => void; openChat: (target: string) => void;
}) {
  const [filter, setFilter] = useState("");
  const [scope, setScope] = useState("project");
  const deferredFilter = useDeferredValue(filter);
  const deferredScope = useDeferredValue(scope);
  const now = useContext(Clock);
  const { byID, activeTasks } = useMemo(() => {
    const active = new Map<string, Task>();
    const index = new Map<string, Task>();
    for (const task of tasks) {
      index.set(task.id, task);
      if (!task.assignee || ["done", "cancelled"].includes(task.status)) continue;
      if (!active.has(task.assignee) || ["in_progress", "blocked", "needs_owner"].includes(task.status)) active.set(task.assignee, task);
    }
    return { byID: index, activeTasks: active };
  }, [tasks]);
  const items = useMemo(() => {
    const needle = deferredFilter.trim().toLowerCase();
    const taskFor = (agent: Agent) => (agent.current_task_id && byID.get(agent.current_task_id)) || activeTasks.get(agent.id);
    const ranked = new Map<string, number>();
    const attention = (agent: Agent) => {
      const task = taskFor(agent);
      return Number(task?.status === "blocked" || task?.status === "needs_owner") * 2 +
        Number(agent.status === "active" && staleAt(task?.last_heartbeat_at ?? agent.last_seen_at, now, task?.lease_expires_at));
    };
    const visible = agents.filter((agent) => {
      if (agent.status === "revoked") return false;
      if (deferredScope === "project" && !taskFor(agent) && agent.role !== "leader") return false;
      return !needle || [agent.id, agent.kind, agent.model, agent.provider, taskFor(agent)?.title]
        .some((item) => item?.toLowerCase().includes(needle));
    });
    visible.forEach((agent) => ranked.set(agent.id, attention(agent)));
    return visible.sort((a, b) => ranked.get(b.id)! - ranked.get(a.id)! || a.id.localeCompare(b.id))
      .map((agent) => ({ id: agent.id, agent, task: taskFor(agent) }));
  }, [agents, byID, activeTasks, deferredFilter, deferredScope, now]);

  return <section className="panel stack">
    <div className="heading">
      <h2>Agent workload <span className="pill">{items.length}</span></h2>
      <div className="actions">
        <input className="filter-input" type="search" aria-label="Filter workload" placeholder="Filter workload"
          value={filter} onChange={(event) => setFilter(event.target.value)} />
        <select className="filter-input" aria-label="Agent scope" value={scope} onChange={(event) => setScope(event.target.value)}>
          <option value="project">Project agents</option><option value="all">All active and paused agents</option>
        </select>
      </div>
    </div>
    {items.length ? <VirtualList name="Agent workload" items={items} estimate={78} className="agent-list"
      render={({ agent, task }) => <AgentRow agent={agent} task={task} agentMessage={recent[`agent:${agent.id}`]}
        taskMessage={task && recent[`task:${task.id}`]} openThread={openThread} openChat={openChat} />} />
      : <p className="empty">No agents match this view.</p>}
  </section>;
}

function CapacitySummary({ revision }: { revision: number }) {
  const capacity = useResource<Capacity>("capacity", revision);
  const items = (capacity.data?.limits ?? []).map((item) => ({ ...item, id: `${item.provider}:${item.model}` }));
  return <section className="panel stack">
    <div className="heading"><h2>Inference capacity</h2><Link href="/capacity">Settings</Link></div>
    <Feedback error={capacity.error} loading={!capacity.data} />
    {capacity.data && <p className="meta">{capacity.data.agents_in_use} / {capacity.data.global_max_agents} agents in use</p>}
    {items.length ? <VirtualList name="Provider and model capacity" estimate={56} items={items} className="capacity-list"
      render={(item) => <div className="capacity-usage">
        <strong>{item.provider} · {item.model}</strong>
        <progress max={Math.max(1, item.max_concurrency)} value={item.in_use} aria-label={`${item.provider} ${item.model} in use`} />
        <span className={item.available <= 0 ? "stale" : "meta"}>{item.in_use} / {item.max_concurrency} · {item.available} free</span>
      </div>} /> : capacity.data && <p className="empty">No provider or model limits.</p>}
  </section>;
}

function SwarmProject({ id, architecture, revisions, recent, openThread }: {
  id: string; architecture: boolean; revisions: Revisions; recent: Record<string, Message>;
  openThread: (thread: Thread) => void;
}) {
  const board = useResource<Board>(`briefs/${encodeURIComponent(id)}/board`, revisions.board);
  const registry = useResource<{ agents: Agent[] }>("agents?limit=1000&include=last_message", revisions.agents);
  const [chat, setChat] = useState<string | null>(null);
  const componentFeed = useResource((architecture || chat) ? `briefs/${encodeURIComponent(id)}/architecture` : null,
    revisions.architecture, optionalResource<{ components: Component[] }>);
  const components = componentFeed.data?.components ?? emptyComponents;
  const agents = registry.data?.agents ?? emptyAgents;
  const taskList = useMemo(() => Object.values(board.data?.columns ?? {}).flatMap((column) => column ?? []), [board.data]);
  const now = useContext(Clock);
  const { complete, total, progress, blocked, stale } = useMemo(() => {
    let complete = 0, total = 0, sum = 0, blocked = 0, stale = 0;
    for (const task of taskList) {
      if (task.status === "cancelled") continue;
      total++;
      if (task.status === "done") { complete++; sum += 100; } else sum += task.progress_pct;
      if (task.status === "blocked" || task.status === "needs_owner") blocked++;
      else if (task.status === "in_progress" && staleAt(task.last_heartbeat_at, now, task.lease_expires_at)) stale++;
    }
    return { complete, total, progress: total ? Math.round(sum / total) : 0, blocked, stale };
  }, [taskList, now]);
  const brief = board.data?.brief;
  const path = `/briefs/${encodeURIComponent(id)}`;

  return <section className="stack project-view">
    <Feedback error={board.error || registry.error} loading={!board.data} />
    {brief && <>
      <div className="heading">
        <div><div className="meta">{brief.id} · {brief.status} · plan {brief.plan_status}</div><h2 className="project-name">{brief.title}</h2></div>
        <div className="actions">
          <button type="button" onClick={() => openThread({ kind: "brief", id, title: brief.title })}>Brief thread</button>
          <button type="button" className="primary" onClick={() => setChat("leader")}>Owner chat</button>
        </div>
      </div>
      <div className="project-stats">
        <div className="panel"><span>Project progress</span><strong>{progress}%</strong><progress max={100} value={progress} aria-label="Project progress" /></div>
        <div className="panel"><span>Tasks done</span><strong>{complete} / {total}</strong></div>
        <div className={`panel ${blocked || stale ? "needs-attention" : ""}`}><span>Needs attention</span><strong>{blocked} blocked · {stale} stale</strong></div>
        <div className={`panel ${brief.budget.max_cost_eur > 0 && brief.cost_used >= brief.budget.max_cost_eur ? "needs-attention" : ""}`}><span>Spend / budget</span><strong>{money(brief.cost_used)} / {money(brief.budget.max_cost_eur)}</strong></div>
      </div>
      <nav className="view-tabs" aria-label="Project view">
        <Link href={path} aria-current={architecture ? undefined : "page"}>Overview</Link>
        <Link href={path + "/architecture"} aria-current={architecture ? "page" : undefined}>Architecture</Link>
        <Link href={path + "/graph"}>Graph</Link>
        <Link href={path + "/report"}>Report</Link>
        <Link href={path + "/replay"}>Replay</Link>
      </nav>
      <div className={`project-layout ${chat ? "has-chat" : ""}`}>
        <div className="stack project-body">
          {architecture ? <Architecture components={components} tasks={taskList} loading={componentFeed.data === undefined}
            error={componentFeed.error} openThread={openThread} openChat={setChat} /> : <>
            <ForecastPanel briefID={id} revision={revisions.board} />
            <Kanban board={board.data!} agents={agents} revision={revisions.board} openThread={openThread} />
            <Workload agents={agents} tasks={taskList} recent={recent} openThread={openThread} openChat={setChat} />
            <ActivityFeed briefID={id} revision={revisions.activity} openThread={openThread} />
            <AutopilotPanel revision={revisions.board} spawnRevision={revisions.agents} />
            <div className="project-detail-grid">
              <CapacitySummary revision={revisions.capacity} />
              <Metrics briefID={id} knownJobs={brief.hillclimb_jobs} revision={revisions.metrics} />
            </div>
          </>}
        </div>
        {chat && <OwnerChat briefID={id} agents={agents} components={components} target={chat} select={setChat}
          revision={revisions.chat} close={() => setChat(null)} />}
      </div>
    </>}
  </section>;
}

export const SwarmOverview = memo(function SwarmOverview({ id, architecture, revisions, recent, openThread }: {
  id?: string; architecture: boolean; revisions: Revisions; recent: Record<string, Message>;
  openThread: (thread: Thread) => void;
}) {
  const briefs = useResource<{ briefs: Brief[] }>("briefs?limit=1000", revisions.briefs);
  const router = useRouter();
  const items = briefs.data?.briefs ?? [];
  const selected = id ?? (items.find((item) => item.status === "open") ?? items[0])?.id;
  return <>
    <div className="heading">
      <h1>Swarm overview</h1>
      <IncidentButton revision={revisions.control} />
      {!!items.length && <label className="project-picker">Project<select value={selected} onChange={(event) => router.push(`/briefs/${encodeURIComponent(event.target.value)}`)}>
        {items.map((brief) => <option key={brief.id} value={brief.id}>{brief.title}</option>)}
      </select></label>}
    </div>
    <Feedback error={briefs.error} loading={!briefs.data && !selected} />
    {!selected && briefs.data && <div className="panel stack"><p>No briefs yet.</p><Link href="/briefs">Create a brief</Link></div>}
    {selected && <SwarmProject key={selected} id={selected} architecture={architecture} revisions={revisions} recent={recent} openThread={openThread} />}
  </>;
});
