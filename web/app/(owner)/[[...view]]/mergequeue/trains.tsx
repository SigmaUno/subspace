"use client";

import { useState } from "react";
import { optionalResource, useResource } from "../../../../lib/client";
import type { MemberResult, Train, TrainList, TrainState } from "../../../../lib/trains-types";
import { EntityLink } from "../shared/components";
import { formatTime } from "../shared/format";
import { EmptyState, ResourceState } from "../shared/states";

const stateLabels: Record<TrainState, string> = {
  building: "Building",
  testing: "Testing",
  merging: "Merging",
  merged: "Merged",
  failed: "Failed",
  aborted: "Aborted",
};

const resultLabels: Record<MemberResult, string> = {
  pending: "Pending",
  merging: "Merging",
  merged: "Merged",
  ejected: "Ejected",
  requeued: "Back in queue",
};

const stateClass: Record<TrainState, string> = {
  building: "paused", testing: "paused", merging: "paused", merged: "active", failed: "revoked", aborted: "revoked",
};

const resultClass: Record<MemberResult, string> = {
  pending: "", merging: "paused", merged: "active", ejected: "revoked", requeued: "paused",
};

const logExcerpt = 4000;
const running: TrainState[] = ["building", "testing", "merging"];

function bisectState(train: Train) {
  if (train.runs <= 1) return null;
  const culprit = (train.members ?? []).find((member) => member.result === "ejected");
  if (running.includes(train.state)) return `Bisecting · run ${train.runs}`;
  return `Bisected over ${train.runs} runs${culprit ? ` · culprit ${culprit.task_id}` : ""}`;
}

function TrainDetail({ train }: { train: Train }) {
  const members = train.members ?? [];
  const bisect = bisectState(train);
  return <div className="stack">
    <div className="heading">
      <h3>{train.id}</h3>
      <span className={`pill ${stateClass[train.state] ?? ""}`}>{stateLabels[train.state] ?? train.state}</span>
    </div>
    <p className="meta">
      {train.repo} · {train.base}
      {train.base_sha && <> · base <code title={train.base_sha}>{train.base_sha.slice(0, 7)}</code></>}
      {train.tip && <> · tip <code title={train.tip}>{train.tip.slice(0, 7)}</code></>}
      {" "}· {train.runs} {train.runs === 1 ? "verify run" : "verify runs"}
    </p>
    {bisect && <p role="status">{bisect}</p>}
    {!members.length && <EmptyState>No members.</EmptyState>}
    {!!members.length && <div className="table-wrap" role="region" aria-label="Train members" tabIndex={0}>
      <table>
        <caption className="meta">Train members in merge order</caption>
        <thead><tr><th>Candidate</th><th>Task</th><th>PR</th><th>Branch</th><th>Commit</th><th>Result</th></tr></thead>
        <tbody>{members.map((member) => (
          <tr key={member.candidate_id}>
            <th scope="row">{member.candidate_id}</th>
            <td><EntityLink kind="task" id={member.task_id} /></td>
            <td>#{member.pr}</td>
            <td className="break">{member.head}</td>
            <td>{member.sha ? <code title={member.sha}>{member.sha.slice(0, 7)}</code> : "—"}</td>
            <td><span className={`pill ${resultClass[member.result] ?? ""}`}>{resultLabels[member.result] ?? member.result}</span></td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
    {train.log ? <pre>{train.log.slice(-logExcerpt)}</pre> : <EmptyState>No log yet.</EmptyState>}
  </div>;
}

export function TrainsPanel({ repo, revision }: { repo: string; revision: number }) {
  const query = new URLSearchParams({ limit: "50" });
  if (repo) query.set("repo", repo);
  const list = useResource(`mergequeue/trains?${query}`, revision, optionalResource<TrainList>);
  const [selected, setSelected] = useState("");
  if (list.data === null) return null;
  const trains = list.data?.trains ?? [];
  const current = trains.find((train) => train.id === selected);

  return <section className="panel stack" aria-labelledby="trains-heading">
    <h2 id="trains-heading">Trains</h2>
    <ResourceState error={list.error} loading={list.data === undefined} retry={list.refresh} rows={3} />
    {list.data && !trains.length && <EmptyState>No merge trains yet.</EmptyState>}
    {!!trains.length && <div className="table-wrap" role="region" aria-label="Trains" tabIndex={0}>
      <table>
        <thead><tr><th>Train</th><th>Repo</th><th>State</th><th>Members</th><th>Bisect</th><th>Updated</th></tr></thead>
        <tbody>{trains.map((train) => (
          <tr key={train.id}>
            <th scope="row"><button className="text-button" aria-pressed={train.id === selected}
              onClick={() => setSelected(train.id === selected ? "" : train.id)}>{train.id}</button></th>
            <td>{train.repo}<div className="meta">{train.base}</div></td>
            <td><span className={`pill ${stateClass[train.state] ?? ""}`}>{stateLabels[train.state] ?? train.state}</span></td>
            <td>{(train.members ?? []).length}</td>
            <td>{bisectState(train) ?? "—"}</td>
            <td>{formatTime(train.updated_at)}</td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
    {current && <TrainDetail key={`${current.id}:${current.updated_at}`} train={current} />}
  </section>;
}
