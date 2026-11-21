"use client";

import { useState } from "react";
import { label, optionalResource, useResource } from "../../../../lib/client";
import type { HelpRequest, HelpRequestList, HelpStatus, ProposedTask } from "../../../../lib/helpreq-types";
import { EntityLink } from "../shared/components";
import { formatTime } from "../shared/format";
import { EmptyState, ResourceState } from "../shared/states";

type Filter = HelpStatus | "all";

const filters: [Filter, string][] = [
  ["open", "Open"], ["accepted", "Accepted"], ["superseded", "Superseded"], ["closed", "Closed"], ["all", "All"],
];

const typeNames = { help: "Help", blocker: "Blocker", split: "Split proposal" } as const;
const statusClass: Record<HelpStatus, string> = { open: "paused", accepted: "active", superseded: "", closed: "" };

function Proposal({ task, index }: { task: ProposedTask; index: number }) {
  return <li className="stack">
    <strong>{index + 1}. {task.title}{task.ref && <span className="meta"> · {task.ref}</span>}</strong>
    <span className="meta">
      {[task.role, task.repo, task.priority ? `priority ${task.priority}` : ""].filter(Boolean).join(" · ") || "same repo as the task"}
      {!!task.depends_on?.length && ` · depends on ${task.depends_on.join(", ")}`}
    </span>
    <p className="preserve">{task.spec}</p>
    {!!task.acceptance_criteria?.length && <ul>
      {task.acceptance_criteria.map((item, position) => <li key={position} className="preserve">{item}</li>)}
    </ul>}
  </li>;
}

function Detail({ request }: { request: HelpRequest }) {
  const proposals = request.proposed_tasks ?? [];
  const created = request.created_tasks ?? [];
  return <section className="panel stack" aria-label={`Details of ${request.id}`}>
    <div className="heading">
      <h2>{request.id}</h2>
      <span className={`pill ${statusClass[request.status] ?? ""}`}>{request.status}</span>
    </div>
    <p className="meta">
      {typeNames[request.type] ?? request.type} · {label(request.kind)}
      {request.needs && ` · needs ${request.needs}`} · {request.agent_id} ({request.role}) ·{" "}
      <EntityLink kind="task" id={request.task_id} /> ·{" "}
      <EntityLink kind="brief" id={request.brief_id} />
    </p>
    <p className="meta">Untrusted agent text: shown as plain text.</p>
    <p className="preserve">{request.details || "No details."}</p>
    {request.type === "split" && <>
      <h3>Proposed tasks</h3>
      {!proposals.length && <EmptyState>No tasks in this proposal.</EmptyState>}
      {!!proposals.length && <ol className="stack">{proposals.map((task, index) => <Proposal key={index} task={task} index={index} />)}</ol>}
      {request.status === "open" && <p className="meta">The leader accepts a split proposal with accept_split; the original task is then cancelled.</p>}
      {!!created.length && <p>Created {created.map((id, index) => <span key={id}>
        {index > 0 && ", "}<EntityLink kind="task" id={id} /></span>)}</p>}
    </>}
  </section>;
}

export function HelpRequestsView({ revision }: { revision: number }) {
  const [status, setStatus] = useState<Filter>("open");
  const [selected, setSelected] = useState("");
  const list = useResource(`help-requests?${new URLSearchParams({ status })}`, revision, optionalResource<HelpRequestList>);
  if (list.data === null) return <EmptyState>Help requests are not available on this server.</EmptyState>;
  const requests = list.data?.help_requests ?? [];
  const current = requests.find((request) => request.id === selected);

  return <>
    <div className="heading"><h1>Help requests</h1></div>
    <div className="actions" role="group" aria-label="Status">
      {filters.map(([id, name]) => <button key={id} className="text-button" aria-pressed={id === status}
        onClick={() => { setStatus(id); setSelected(""); }}>{name}</button>)}
    </div>
    <ResourceState error={list.error} loading={list.data === undefined} retry={list.refresh} rows={3} />
    {list.data && !requests.length && <EmptyState>No {status === "all" ? "" : `${status} `}help requests.</EmptyState>}
    {!!requests.length && <div className="table-wrap">
      <table>
        <thead><tr><th>Request</th><th>Type</th><th>Task</th><th>Agent</th><th>Status</th><th>Created</th></tr></thead>
        <tbody>{requests.map((request) => (
          <tr key={request.id}>
            <th scope="row"><button className="text-button" aria-pressed={request.id === selected}
              onClick={() => setSelected(request.id === selected ? "" : request.id)}>{request.id}</button></th>
            <td>{typeNames[request.type] ?? request.type}<div className="meta">{label(request.kind)}{request.needs && ` · needs ${request.needs}`}</div></td>
            <td><EntityLink kind="task" id={request.task_id} /></td>
            <td>{request.agent_id}</td>
            <td><span className={`pill ${statusClass[request.status] ?? ""}`}>{request.status}</span></td>
            <td>{formatTime(request.created_at)}</td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
    {current && <Detail request={current} />}
  </>;
}
