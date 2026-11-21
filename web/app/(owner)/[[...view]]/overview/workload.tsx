"use client";

import { memo, useContext, useDeferredValue, useMemo, useState } from "react";
import { api, label, useResource } from "../../../../lib/client";
import type { Agent, Message, Task, Thread } from "../../../../lib/types";
import { elapsedMinutes } from "../../../../lib/duration";
import { Clock, Heartbeat, staleAt } from "../shared/components";
import { EmptyState } from "../shared/states";
import { VirtualList } from "../shared/virtual";

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
        ? <><span className="pill" data-status={task.status}>{label(task.status)}</span> {task.progress_pct}% · {elapsedMinutes(task.minutes_used)}</>
        : `${agent.role} · ${agent.status}`}</div>
    </div>
    <div><Heartbeat time={heartbeat} watch={agent.status === "active"} expires={task?.lease_expires_at} />
      <div className="meta">{agent.assigned_count} assigned</div></div>
    <div className="activity"><p className="ellipsis" title={line}>{line || "No recent messages"}</p>
      <button className="text-button" type="button" onClick={() => openChat(`agent:${agent.id}`)}>Message agent</button></div>
  </article>;
});

export function Workload({ agents, tasks, recent, openThread, openChat }: {
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
      : <EmptyState>No agents match this view.</EmptyState>}
  </section>;
}
