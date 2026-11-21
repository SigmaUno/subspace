"use client";

import Link from "next/link";
import { useState } from "react";
import { api, optionalResource, useResource, useSubmit, value } from "../../../lib/client";
import type { Brief } from "../../../lib/types";
import type { InstructionDiff, InstructionKind, InstructionLog, InstructionVersion } from "../../../lib/instructions-types";
import { Feedback } from "./components";
import { EmptyState, ResourceState } from "./states";

const kinds: [InstructionKind, string][] = [["rules", "Rules"], ["style", "Style"], ["templates", "Templates"]];
const diffMarks = { equal: " ", add: "+", delete: "-" };

function documentPath(scope: string, kind: InstructionKind) {
  return `instructions/${encodeURIComponent(scope)}/${kind}`;
}

function VersionDiff({ scope, kind, version }: { scope: string; kind: InstructionKind; version: number }) {
  const diff = useResource<InstructionDiff>(`${documentPath(scope, kind)}/diff?from=${version - 1}&to=${version}`, 0);
  return <section className="stack">
    <h3>Changes from v{version - 1} to v{version}</h3>
    <ResourceState error={diff.error} loading={!diff.data} retry={diff.refresh} rows={3} />
    {diff.data && <pre className="preserve">{diff.data.lines.map((line, index) => (
      <div key={index} className={line.op}>{diffMarks[line.op]} {line.text}</div>
    ))}</pre>}
  </section>;
}

function Editor({ scope, kind, text, reload }: { scope: string; kind: InstructionKind; text: string; reload: () => void }) {
  const [draft, setDraft] = useState(text);
  const form = useSubmit();
  return <form className="panel stack" data-write onSubmit={(event) => form.submit(event, async (fields) => {
    await api<InstructionVersion>(documentPath(scope, kind), "PUT", { text: draft, message: value(fields, "message") });
    reload();
  })}>
    <div className="form-grid">
      <label>Markdown<textarea rows={18} value={draft} onChange={(event) => setDraft(event.target.value)} /></label>
      <div className="stack">
        <span className="meta">Preview</span>
        <div className="preserve">{draft || <span className="empty">Nothing yet.</span>}</div>
      </div>
    </div>
    <label>Version message<input name="message" placeholder="What changed?" /></label>
    <Feedback error={form.error} />
    <div><button className="primary" disabled={form.busy || draft === text}>Save as new version</button></div>
  </form>;
}

function InstructionDocument({ scope, kind }: { scope: string; kind: InstructionKind }) {
  const path = documentPath(scope, kind);
  const current = useResource(path, 0, optionalResource<InstructionVersion>);
  const log = useResource<InstructionLog>(`${path}/versions`, 0);
  const [selected, setSelected] = useState(0);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const versions = log.data?.versions ?? [];
  const head = current.data?.version ?? 0;
  const focus = selected || head;

  function reload() {
    setSelected(0);
    current.refresh();
    log.refresh();
  }

  async function rollback(to: number) {
    setPending(true);
    setError("");
    try {
      await api<InstructionVersion>(`${path}/rollback`, "POST", { to, message: "" });
      reload();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Rollback failed.");
    } finally {
      setPending(false);
    }
  }

  return <section className="stack">
    <Feedback error={error} />
    <ResourceState error={current.error || log.error} loading={current.data === undefined}
      retry={() => { current.refresh(); log.refresh(); }} rows={6} />
    {current.data !== undefined && <Editor key={head} scope={scope} kind={kind} text={current.data?.text ?? ""} reload={reload} />}
    {log.data !== undefined && <section className="panel stack">
      <h2>Version history</h2>
      {!versions.length && <EmptyState>No versions yet.</EmptyState>}
      {!!versions.length && <div className="table-wrap">
        <table>
          <thead><tr><th>Version</th><th>Author</th><th>Message</th><th>Saved</th><th><span className="meta">Action</span></th></tr></thead>
          <tbody>{versions.map((item) => (
            <tr key={item.version}>
              <th scope="row"><button className="text-button" aria-pressed={item.version === focus}
                onClick={() => setSelected(item.version)}>v{item.version}</button>
                {item.version === head && <span className="pill">current</span>}</th>
              <td>{item.author}</td>
              <td>{item.message || "—"}</td>
              <td><time dateTime={item.created_at}>{new Date(item.created_at).toLocaleString()}</time></td>
              <td>{item.version !== head && <button data-write disabled={pending} onClick={() => rollback(item.version)}>Roll back</button>}</td>
            </tr>
          ))}</tbody>
        </table>
      </div>}
      {focus > 1 && <VersionDiff key={focus} scope={scope} kind={kind} version={focus} />}
      {focus === 1 && <EmptyState>First version, nothing earlier to compare.</EmptyState>}
    </section>}
  </section>;
}

export function InstructionsPage() {
  const [scope, setScope] = useState("global");
  const [kind, setKind] = useState<InstructionKind>("rules");
  const briefs = useResource<{ briefs: Brief[] }>("briefs", 0);
  const scopes = [{ id: "global", name: "Global" }, ...(briefs.data?.briefs ?? []).map((brief) => ({ id: `project:${brief.id}`, name: brief.id }))];

  return <>
    <header className="topbar">
      <Link className="brand" href="/briefs"><span className="brand-mark" aria-hidden="true" />Subspace</Link>
      <nav aria-label="Workspace">
        <Link href="/briefs">Briefs</Link>
        <Link href="/instructions" aria-current="page">Instructions</Link>
      </nav>
    </header>
    <main className="workspace stack">
      <div className="heading"><h1>Instructions</h1></div>
      <ResourceState error={briefs.error} retry={briefs.refresh} />
      <div className="actions" role="group" aria-label="Scope">
        {scopes.map((item) => <button key={item.id} className="text-button" aria-pressed={item.id === scope}
          onClick={() => setScope(item.id)}>{item.name}</button>)}
      </div>
      <div className="actions" role="group" aria-label="Kind">
        {kinds.map(([id, name]) => <button key={id} className="text-button" aria-pressed={id === kind}
          onClick={() => setKind(id)}>{name}</button>)}
      </div>
      <InstructionDocument key={`${scope}/${kind}`} scope={scope} kind={kind} />
    </main>
  </>;
}
