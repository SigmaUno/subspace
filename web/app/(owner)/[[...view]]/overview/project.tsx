"use client";

import Link from "next/link";
import { useContext, useMemo, useState } from "react";
import { money, optionalResource, useResource } from "../../../../lib/client";
import type { Agent, Board, Message, Thread } from "../../../../lib/types";
import type { Component } from "../../../../lib/swarm";
import { Kanban } from "../board";
import { InsightsTile } from "../insights";
import { Clock, staleAt } from "../shared/components";
import type { Revisions } from "../shared/live";
import { ResourceState } from "../shared/states";
import { ActivityFeed } from "./activity-feed";
import { Architecture } from "./architecture";
import { AutopilotPanel } from "./autopilot";
import { CapacitySummary } from "./capacity";
import { OwnerChat } from "./chat";
import { ForecastPanel } from "./forecast";
import { Metrics } from "./metrics";
import { Workload } from "./workload";

const emptyComponents: Component[] = [];
const emptyAgents: Agent[] = [];

export function SwarmProject({ id, architecture, revisions, recent, openThread }: {
  id: string; architecture: boolean; revisions: Revisions; recent: Record<string, Message>;
  openThread: (thread: Thread) => void;
}) {
  const board = useResource<Board>(`briefs/${encodeURIComponent(id)}/board`, revisions.board);
  const registry = useResource<{ agents: Agent[] }>("agents?limit=1000&include=last_message", revisions.agents);
  const [chat, setChat] = useState<string | null>(null);
  const componentFeed = useResource(`briefs/${encodeURIComponent(id)}/architecture`,
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
    <ResourceState error={board.error || registry.error} loading={!board.data} retry={() => { board.refresh(); registry.refresh(); }} rows={4} />
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
        <InsightsTile revision={revisions.agents + revisions.board} />
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
            <Kanban board={board.data!} agents={agents} components={components} revision={revisions.board} openThread={openThread} />
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
