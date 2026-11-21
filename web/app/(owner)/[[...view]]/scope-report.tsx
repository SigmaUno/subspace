"use client";

import { optionalResource, useResource } from "../../../lib/client";
import type { ScopeReport } from "../../../lib/scope-types";
import { formatTime } from "./format";
import { EmptyState } from "./states";

const severityClass = { error: "revoked", warning: "paused" } as const;

export function ScopePanel({ taskID, revision }: { taskID: string; revision: number }) {
  const report = useResource(`tasks/${encodeURIComponent(taskID)}/scope`, revision, optionalResource<ScopeReport>);
  const current = report.data;
  if (!current) return null;
  const findings = current.findings ?? [];

  return <section className="panel stack" aria-labelledby="scope-heading">
    <div className="heading">
      <h2 id="scope-heading">Scope and quality gate</h2>
      <span className={`pill ${current.merge_eligible ? "active" : "revoked"}`}>
        {current.merge_eligible ? "Merge eligible" : "Blocked by scope errors"}
      </span>
    </div>
    <p className="meta">
      {current.files} {current.files === 1 ? "file" : "files"} · +{current.additions} −{current.deletions}
      {current.truncated && " · file list truncated"}
      {current.head_sha && <> · <code title={current.head_sha}>{current.head_sha.slice(0, 7)}</code></>}
      {current.checked_at && <> · checked {formatTime(current.checked_at)}</>}
    </p>
    {!findings.length && <EmptyState>No scope findings.</EmptyState>}
    {!!findings.length && <div className="table-wrap">
      <table>
        <caption className="meta">Scope findings</caption>
        <thead><tr><th>Rule</th><th>Severity</th><th>Finding</th></tr></thead>
        <tbody>{findings.map((finding, index) => (
          <tr key={`${finding.rule}-${index}`}>
            <th scope="row"><code>{finding.rule}</code></th>
            <td><span className={`pill ${severityClass[finding.severity] ?? ""}`}>{finding.severity}</span></td>
            <td>
              <span className="break">{finding.message}</span>
              {!!finding.paths?.length && <ul className="meta">
                {finding.paths.map((path) => <li key={path}><code className="break">{path}</code></li>)}
              </ul>}
            </td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
  </section>;
}
