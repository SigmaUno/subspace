"use client";

import { useState } from "react";
import { api, label, useSubmit } from "../../../../lib/client";
import type { RetentionReport, RetentionSegment, RetentionStatus, RetentionVerify } from "../../../../lib/settings-types";
import { formatSize, formatTime } from "../shared/format";
import { Feedback } from "../shared/components";
import { EmptyState } from "../shared/states";
import { useAction, useConfirm, useToast } from "../shared/ui";

const statusClass = { pending: "paused", archived: "active", restored: "" } as const;
const shown = 50;

function Run({ report }: { report: RetentionReport }) {
  const rows = Object.entries(report.rows ?? {});
  return <p className="meta">
    Last run {formatTime(report.at)} · {report.archived?.length ?? 0} segments archived
    {rows.length ? ` (${rows.map(([kind, count]) => `${kind} ${count}`).join(", ")} rows)` : ""}
    {report.resumed ? ` · ${report.resumed} resumed` : ""}
    {report.error ? ` · ${report.error}` : ""}
  </p>;
}

export function RetentionPanel({ status, refresh }: { status: RetentionStatus; refresh: () => void }) {
  const [verify, setVerify] = useState<RetentionVerify>();
  const action = useAction();
  const confirm = useConfirm();
  const toast = useToast();
  const run = useSubmit();
  const check = useSubmit();
  const segments = [...(status.segments ?? [])].sort((a, b) => b.id - a.id);
  const policy = Object.entries(status.policy);

  async function restore(segment: RetentionSegment) {
    const accepted = await confirm({
      title: `Restore segment ${segment.id}?`,
      message: `${segment.rows} ${segment.kind} rows are written back into the database.`,
      confirm: "Restore",
      danger: false,
    });
    if (accepted && await action.run(String(segment.id), () => api<RetentionSegment>(`retention/segments/${segment.id}/restore`, "POST", {}),
      `Restored segment ${segment.id}.`)) refresh();
  }

  return <>
    <section className="panel stack">
      <div className="heading"><h2>Data retention</h2><span className="meta">Archive directory {status.dir}</span></div>
      {!policy.length && <EmptyState>No retention policy reported.</EmptyState>}
      {!!policy.length && <div className="table-wrap">
        <table>
          <thead><tr><th>Policy</th><th>Value</th></tr></thead>
          <tbody>{policy.map(([key, setting]) => <tr key={key}>
            <th scope="row">{label(key)}</th><td>{String(setting) || "—"}</td>
          </tr>)}</tbody>
        </table>
      </div>}
      <p className="meta">Audit anchor: event {status.anchor.id}{status.anchor.hash ? ` · ${status.anchor.hash.slice(0, 16)}…` : ""}. Days of 0 keep that kind forever.</p>
      {status.last_run ? <Run report={status.last_run} /> : <p className="meta">No archive run since the server started.</p>}
      <div className="actions">
        <form data-write onSubmit={(event) => run.submit(event, async () => {
          await api<RetentionReport>("retention/run", "POST", {});
          toast("Archive run finished.");
          refresh();
        })}><button className="primary" disabled={run.busy}>Run now</button></form>
        <form onSubmit={(event) => check.submit(event, async () => {
          setVerify(await api<RetentionVerify>("retention/verify"));
        })}><button disabled={check.busy}>Verify archive</button></form>
      </div>
      <Feedback error={run.error || check.error} />
      {verify && <p role="status" className={verify.ok ? "meta" : "error"}>
        {verify.ok
          ? `Archive verified: ${verify.segments} segments, ${verify.rows} rows, audit chain of ${verify.chain.events} events intact.`
          : `Verification failed${verify.broken_segment ? ` at segment ${verify.broken_segment}` : ""}${verify.error ? `: ${verify.error}` : "."}`}
      </p>}
    </section>
    <section className="panel stack">
      <div className="heading"><h2>Segments</h2>
        {segments.length > shown && <span className="meta">Newest {shown} of {segments.length}</span>}</div>
      {!segments.length && <EmptyState>Nothing archived yet.</EmptyState>}
      {!!segments.length && <div className="table-wrap">
        <table>
          <thead><tr><th>Segment</th><th>Kind</th><th>Status</th><th>Rows</th><th>Size</th><th>Cutoff</th><th>Created</th><th><span className="meta">Action</span></th></tr></thead>
          <tbody>{segments.slice(0, shown).map((segment) => <tr key={segment.id}>
            <th scope="row">{segment.id} <span className="meta">{segment.kind} #{segment.seq}</span></th>
            <td>{segment.kind}</td>
            <td><span className={`pill ${statusClass[segment.status]}`}>{segment.status}</span></td>
            <td>{segment.rows}</td>
            <td>{formatSize(segment.bytes)}</td>
            <td>{formatTime(segment.cutoff)}</td>
            <td>{formatTime(segment.created_at)}</td>
            <td>{segment.status === "archived" && <button type="button" data-write disabled={action.busy === String(segment.id)}
              onClick={() => restore(segment)} aria-label={`Restore segment ${segment.id}`}>Restore</button>}</td>
          </tr>)}</tbody>
        </table>
      </div>}
    </section>
  </>;
}
