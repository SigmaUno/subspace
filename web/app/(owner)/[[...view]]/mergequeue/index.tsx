"use client";

import { useState } from "react";
import { api, useResource, useSubmit } from "../../../../lib/client";
import type { Candidate, CandidateList, CandidateState, EnqueueRequest } from "../../../../lib/mergequeue-types";
import type { Task } from "../../../../lib/types";
import { Feedback } from "../shared/components";
import { formatTime } from "../shared/format";
import { useLive } from "../shared/live";
import { EmptyState, ResourceState } from "../shared/states";
import { PageShell } from "../shell";
import { TrainsPanel } from "./trains";

const stateLabels: Record<CandidateState, string> = {
  queued: "Queued",
  running: "Testing",
  waiting: "Awaiting approval",
  merged: "Merged",
  ejected: "Failed",
};

const logExcerpt = 4000;

function prNumber(url: string | null) {
  const match = url?.match(/\/pulls?\/(\d+)/);
  return match ? Number(match[1]) : null;
}

function EnqueueForm({ task, base, queued }: { task: Task; base: string; queued: () => void }) {
  const form = useSubmit();
  const pr = prNumber(task.pr_url);
  return <form data-write onSubmit={(event) => form.submit(event, async () => {
    const body: EnqueueRequest = { repo: task.repo, pr: pr ?? 0, head: task.branch, base: base.trim(), task_id: task.id };
    await api<Candidate>("mergequeue/candidates", "POST", body);
    queued();
  })}>
    <div className="actions">
      <button className="primary" disabled={form.busy || pr === null || !base.trim()}>Enqueue</button>
    </div>
    <Feedback error={form.error} />
  </form>;
}

function CandidateDetail({ candidate }: { candidate: Candidate }) {
  return <div className="stack">
    <div className="heading"><h2>{candidate.id}</h2><span className="pill">{stateLabels[candidate.state]}</span></div>
    {candidate.merged_sha && <p className="meta">Merged as <code>{candidate.merged_sha}</code></p>}
    {candidate.log ? <pre>{candidate.log.slice(-logExcerpt)}</pre> : <EmptyState>No log yet.</EmptyState>}
  </div>;
}

export function MergeQueuePage() {
  const { revisions, status } = useLive("all");
  const candidates = useResource<CandidateList>("mergequeue/candidates", revisions.board);
  const approved = useResource<{ tasks: Task[] }>("tasks?status=approved", revisions.board);
  const [repo, setRepo] = useState("");
  const [base, setBase] = useState("main");
  const [selected, setSelected] = useState("");

  const all = candidates.data?.candidates ?? [];
  const approvedTasks = approved.data?.tasks ?? [];
  const repos = [...new Set([...all.map((item) => item.repo), ...approvedTasks.map((task) => task.repo)])].sort();
  const queue = all.filter((item) => !repo || item.repo === repo);
  const tasks = approvedTasks.filter((task) => !repo || task.repo === repo);
  const current = queue.find((item) => item.id === selected);

  return <PageShell section="mergequeue" status={status}>
    <div className="heading">
      <h1>Merge queue</h1>
      <div className="actions">
        <label className="inline">Repo
          <select value={repo} onChange={(event) => setRepo(event.target.value)}>
            <option value="">All repos</option>
            {repos.map((name) => <option key={name} value={name}>{name}</option>)}
          </select>
        </label>
        <label className="inline">Base branch
          <input value={base} onChange={(event) => setBase(event.target.value)} />
        </label>
      </div>
    </div>
    <ResourceState error={candidates.error || approved.error} loading={!candidates.data} retry={() => { candidates.refresh(); approved.refresh(); }} rows={4} />

    <section className="panel stack">
      <h2>Approved tasks</h2>
      {approved.data && !tasks.length && <EmptyState>No approved tasks waiting for the queue.</EmptyState>}
      {!!tasks.length && <div className="table-wrap" role="region" aria-label="Approved tasks" tabIndex={0}>
        <table>
          <thead><tr><th>Task</th><th>Repo</th><th>Branch</th><th>PR</th><th>Queue</th></tr></thead>
          <tbody>{tasks.map((task) => (
            <tr key={task.id}>
              <th scope="row">{task.title}<div className="meta">{task.id}</div></th>
              <td>{task.repo}</td>
              <td>{task.branch}</td>
              <td>{task.pr_url ?? "No PR"}</td>
              <td><EnqueueForm task={task} base={base} queued={candidates.refresh} /></td>
            </tr>
          ))}</tbody>
        </table>
      </div>}
    </section>

    <section className="panel stack">
      <h2>Candidates</h2>
      {candidates.data && !queue.length && <EmptyState>The queue is empty.</EmptyState>}
      {!!queue.length && <div className="table-wrap" role="region" aria-label="Candidates" tabIndex={0}>
        <table>
          <thead><tr><th>Candidate</th><th>Repo</th><th>Rebase</th><th>State</th><th>Updated</th></tr></thead>
          <tbody>{queue.map((item) => (
            <tr key={item.id}>
              <th scope="row"><button className="text-button" aria-pressed={item.id === selected}
                onClick={() => setSelected(item.id)}>{item.id}</button>
                <div className="meta">PR #{item.pr} · {item.task_id}</div></th>
              <td>{item.repo}</td>
              <td>{item.head} → {item.base}</td>
              <td><span className="pill">{stateLabels[item.state]}</span></td>
              <td>{formatTime(item.updated_at)}</td>
            </tr>
          ))}</tbody>
        </table>
      </div>}
      {current && <CandidateDetail key={current.id} candidate={current} />}
    </section>

    <TrainsPanel repo={repo} revision={revisions.board} />
  </PageShell>;
}
