"use client";

import { useState } from "react";
import { label, usePagedResource, useResource } from "../../../../lib/client";
import type { AuditEvent, TaskDetail, TaskList } from "../../../../lib/task-detail-types";
import type { Message } from "../../../../lib/types";
import { EntityLink } from "../shared/components";
import { TaskGates } from "../gates";
import { useLive } from "../shared/live";
import { usePR } from "../pr-status";
import { AgentTerminal } from "../terminal";
import { PageShell } from "../shell";
import { EmptyState, ResourceState } from "../shared/states";
import { TaskTimeline, TaskThread } from "./activity";
import { OwnerActions } from "./owner-actions";
import { TaskOverview } from "./overview";
import { ScopePanel } from "./scope-report";
import { statusName, timeline } from "./timeline";
import { TaskTranscript } from "./transcript";
import "./transcript.css";

type Tab = "details" | "transcript";

const startTab = (): Tab => typeof window !== "undefined" && window.location.hash === "#transcript" ? "transcript" : "details";

export function TaskDetailPage({ id }: { id: string }) {
  const { revisions, connected } = useLive(`task:${id},thread:task:${id}`);
  const path = encodeURIComponent(id);
  const task = useResource<TaskDetail>(`tasks/${path}`, revisions.board);
  const briefID = task.data?.brief_id;
  const siblings = useResource<TaskList>(
    briefID ? `tasks?${new URLSearchParams({ brief_id: briefID, limit: "1000" })}` : null, revisions.board);
  const audit = usePagedResource<AuditEvent>(
    `audit?${new URLSearchParams({ task_id: id })}`, "events", revisions.board + revisions.thread);
  const messages = usePagedResource<Message>(`threads/task/${path}/messages`, "messages", revisions.thread, 200);
  const prStatus = usePR(id, revisions.board);
  const [tab, setTab] = useState<Tab>(startTab);

  const current = task.data;
  const known = new Map(siblings.data?.tasks?.map((item) => [item.id, item]));
  const entries = timeline(audit.items, messages.items);

  function changed(updated: TaskDetail) {
    task.setData(updated);
    audit.refresh();
    messages.refresh();
  }

  return <PageShell section="briefs" status={connected ? "connected" : "reconnecting"}>
    <ResourceState error={task.error} loading={!current && !task.error} retry={task.refresh} rows={4} />
    {current && <>
      <div className="heading">
        <div>
          <p className="meta">{current.id} · <EntityLink kind="brief" id={current.brief_id} /> · {current.role}</p>
          <h1>{current.title}</h1>
        </div>
        <span className="pill" data-status={current.status}>{statusName(current.status)}</span>
      </div>
      {current.blocked_reason && <div className="notice" role="status">{label(current.blocked_reason)}</div>}

      <div className="tabs" role="tablist" aria-label="Task sections">
        {(["details", "transcript"] as const).map((name) =>
          <button key={name} type="button" role="tab" id={`task-tab-${name}`} aria-selected={tab === name}
            aria-controls={`task-panel-${name}`} onClick={() => setTab(name)}>{label(name)}</button>)}
      </div>

      {tab === "transcript" && <div role="tabpanel" id="task-panel-transcript" aria-labelledby="task-tab-transcript">
        <TaskTranscript taskID={current.id} revision={revisions.board} />
      </div>}

      {tab === "details" && <div className="stack" role="tabpanel" id="task-panel-details" aria-labelledby="task-tab-details">
        <TaskOverview task={current} prStatus={prStatus} />

        <TaskGates taskID={current.id} revision={revisions.board + revisions.thread} />

        <section className="panel stack">
          <h2>Specification</h2>
          <p className="preserve">{current.spec || "No specification."}</p>
          <h3>Acceptance criteria</h3>
          {current.acceptance_criteria?.length
            ? <ul>{current.acceptance_criteria.map((item, index) => <li key={index} className="preserve">{item}</li>)}</ul>
            : <EmptyState>No acceptance criteria.</EmptyState>}
          <h3>Dependencies</h3>
          {current.depends_on?.length
            ? <ul>{current.depends_on.map((dependency) => {
              const other = known.get(dependency);
              return <li key={dependency}>
                <EntityLink kind="task" id={dependency} />
                {other && <> · {other.title} <span className="pill" data-status={other.status}>{statusName(other.status)}</span></>}
              </li>;
            })}</ul>
            : <EmptyState>No dependencies.</EmptyState>}
        </section>

        <ScopePanel taskID={current.id} revision={revisions.board} />

        <OwnerActions task={current} changed={changed} />

        {current.assignee && <AgentTerminal agent={current.assignee} taskID={current.id} />}

        <TaskTimeline audit={audit} entries={entries} />

        <TaskThread path={path} messages={messages} />
      </div>}
    </>}
  </PageShell>;
}
