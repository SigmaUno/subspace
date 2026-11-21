"use client";

import { money } from "../../../lib/client";
import type { DigestPreview } from "../../../lib/digest-types";
import { EntityLink } from "./components";
import { EmptyState } from "./states";

const digestWindows: [string, string][] = [
  ["24h", "Last 24 hours"], ["72h", "Last 3 days"], ["168h", "Last 7 days"], ["744h", "Last 31 days"],
];

const severityClass = { high: "revoked", medium: "paused" } as const;

const stamp = (iso: string) => new Date(iso).toLocaleString();

export function DigestPanel({ preview, range, setRange }: {
  preview: DigestPreview;
  range: string;
  setRange: (range: string) => void;
}) {
  const { digest } = preview;
  const risks = digest.risks ?? [];
  const titles = digest.decisions.titles ?? [];
  const flapping = digest.flapping_agents ?? [];
  const ready = digest.merge_ready ?? [];

  return <section className="panel stack" aria-labelledby="digest-heading">
    <div className="heading">
      <h2 id="digest-heading">Digest preview</h2>
      <label className="inline">Window
        <select value={range} onChange={(event) => setRange(event.target.value)}>
          {digestWindows.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
        </select>
      </label>
    </div>
    <p className="meta">
      <time dateTime={digest.since}>{stamp(digest.since)}</time> to <time dateTime={digest.until}>{stamp(digest.until)}</time>.
      This is what the notification sinks would receive.
    </p>
    <h3>{preview.title}</h3>
    <dl className="stack">
      <div className="heading"><dt className="meta">Tasks done</dt><dd>{digest.tasks.done}</dd></div>
      <div className="heading"><dt className="meta">Tasks created</dt><dd>{digest.tasks.created}</dd></div>
      <div className="heading"><dt className="meta">Tasks blocked</dt><dd>{digest.tasks.blocked}</dd></div>
      <div className="heading"><dt className="meta">Cost</dt><dd>{money(digest.cost_eur)}</dd></div>
      <div className="heading"><dt className="meta">Tokens</dt><dd>{digest.tokens.toLocaleString()}</dd></div>
      <div className="heading"><dt className="meta">Pending decisions</dt><dd>{digest.decisions.count}</dd></div>
    </dl>
    <h3>Risks</h3>
    {!risks.length && <EmptyState>No risks.</EmptyState>}
    {!!risks.length && <ul className="stack">
      {risks.map((risk) => <li key={`${risk.source}-${risk.subject}`}>
        <span className={`pill ${severityClass[risk.severity] ?? ""}`}>{risk.severity}</span>{" "}
        <span className="meta">{risk.source}</span> {risk.message}
      </li>)}
    </ul>}
    {!!titles.length && <>
      <h3>Top decisions</h3>
      <ul>{titles.map((title) => <li key={title}>{title}</li>)}</ul>
    </>}
    {!!flapping.length && <>
      <h3>Flapping agents</h3>
      <ul>{flapping.map((id) => <li key={id}><EntityLink kind="agent" id={id} /></li>)}</ul>
    </>}
    {!!ready.length && <>
      <h3>Merge-ready</h3>
      <ul>{ready.map((id) => <li key={id}><EntityLink kind="task" id={id} /></li>)}</ul>
    </>}
    <h3>Message text</h3>
    <pre>{preview.text}</pre>
  </section>;
}
