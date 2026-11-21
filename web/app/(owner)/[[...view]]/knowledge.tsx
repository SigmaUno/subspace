"use client";

import { useState } from "react";
import { api, commaList, useResource, useSubmit, value } from "../../../lib/client";
import type { Brief } from "../../../lib/types";
import type { KnowledgeEntry, KnowledgeKind, KnowledgeList, KnowledgeStatus } from "../../../lib/knowledge-types";
import { Feedback } from "./components";
import { EmptyState, ResourceState } from "./states";
import { PageShell } from "./shell";

const kinds: KnowledgeKind[] = ["decision", "convention", "pitfall", "howto", "glossary"];
const views: [KnowledgeStatus | "", string][] = [["active", "Active"], ["pending", "Moderation queue"], ["superseded", "Superseded"], ["", "All"]];

function entryPath(id: string, action = "") {
  return `knowledge/${encodeURIComponent(id)}${action}`;
}

function EntryFields({ entry, briefs }: { entry?: KnowledgeEntry; briefs?: Brief[] }) {
  return <>
    <div className="form-grid">
      {briefs && <label>Brief<select name="brief_id" required defaultValue="">
        <option value="" disabled>Choose a brief</option>
        {briefs.map((brief) => <option key={brief.id} value={brief.id}>{brief.id} · {brief.title}</option>)}
      </select></label>}
      <label>Kind<select name="kind" defaultValue={entry?.kind ?? "decision"}>
        {kinds.map((kind) => <option key={kind} value={kind}>{kind}</option>)}
      </select></label>
      <label>Title<input name="title" required maxLength={200} defaultValue={entry?.title} /></label>
      <label>Tags<input name="tags" placeholder="store, migrations" defaultValue={entry?.tags.join(", ")} /></label>
    </div>
    <label>Body (markdown, at most 8 KiB)<textarea name="body" rows={10} required defaultValue={entry?.body} /></label>
  </>;
}

function NewEntry({ briefs, created }: { briefs: Brief[]; created: (entry: KnowledgeEntry) => void }) {
  const form = useSubmit();
  return <form className="panel stack" onSubmit={(event) => form.submit(event, async (fields, element) => {
    const entry = await api<KnowledgeEntry>("knowledge", "POST", {
      brief_id: value(fields, "brief_id"), kind: value(fields, "kind"), title: value(fields, "title"),
      body: String(fields.get("body") ?? ""), tags: commaList(value(fields, "tags")),
    });
    element.reset();
    created(entry);
  })}>
    <h2>New entry</h2>
    <EntryFields briefs={briefs} />
    <Feedback error={form.error} />
    <div><button className="primary" disabled={form.busy}>Add entry</button></div>
  </form>;
}

function EntryDetail({ entry, changed, removed }: { entry: KnowledgeEntry; changed: (entry: KnowledgeEntry) => void; removed: () => void }) {
  const [editing, setEditing] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const form = useSubmit();

  async function run(action: () => Promise<void>) {
    setPending(true);
    setError("");
    try {
      await action();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Request failed.");
    } finally {
      setPending(false);
    }
  }

  const act = (action: string, body: unknown) => run(async () => changed(await api<KnowledgeEntry>(entryPath(entry.id, action), "POST", body)));

  return <section className="panel stack">
    <div className="heading">
      <div>
        <h2>{entry.title}</h2>
        <p className="meta">{entry.id} · {entry.brief_id} · {entry.kind} · by {entry.author} ({entry.author_role}) ·{" "}
          <time dateTime={entry.created_at}>{new Date(entry.created_at).toLocaleString()}</time></p>
      </div>
      <div className="actions">
        <span className="pill">{entry.status}</span>
        {entry.pinned && <span className="pill">pinned</span>}
        {entry.tags.map((tag) => <span key={tag} className="pill">{tag}</span>)}
      </div>
    </div>
    {entry.supersedes && <p className="meta">Supersedes {entry.supersedes}</p>}
    {entry.superseded_by && <p className="meta">Superseded by {entry.superseded_by}</p>}
    <p className="meta">Untrusted project note: shown as plain text.</p>
    <div className="preserve">{entry.body}</div>
    <Feedback error={error} />
    <div className="actions">
      {entry.status === "pending" && <button className="primary" disabled={pending} onClick={() => act("/approve", {})}>Approve</button>}
      <button disabled={pending} onClick={() => act("/pin", { pinned: !entry.pinned })}>{entry.pinned ? "Unpin" : "Pin"}</button>
      {entry.status !== "superseded" && <button disabled={pending} onClick={() => act("/supersede", { by: "" })}>Mark superseded</button>}
      <button disabled={pending} aria-expanded={editing} onClick={() => setEditing(!editing)}>Edit</button>
      <button className="danger" disabled={pending} onClick={() => run(async () => {
        await api<void>(entryPath(entry.id), "DELETE");
        removed();
      })}>{entry.status === "pending" ? "Reject" : "Delete"}</button>
    </div>
    {editing && <form className="stack" onSubmit={(event) => form.submit(event, async (fields) => {
      changed(await api<KnowledgeEntry>(entryPath(entry.id), "PATCH", {
        kind: value(fields, "kind"), title: value(fields, "title"),
        body: String(fields.get("body") ?? ""), tags: commaList(value(fields, "tags")),
      }));
      setEditing(false);
    })}>
      <EntryFields entry={entry} />
      <Feedback error={form.error} />
      <div><button className="primary" disabled={form.busy}>Save</button></div>
    </form>}
  </section>;
}

export function KnowledgePage() {
  const [brief, setBrief] = useState("");
  const [kind, setKind] = useState<KnowledgeKind | "">("");
  const [status, setStatus] = useState<KnowledgeStatus | "">("active");
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState("");
  const briefs = useResource<{ briefs: Brief[] }>("briefs", 0);
  const params = new URLSearchParams({ limit: "100" });
  if (brief) params.set("brief_id", brief);
  if (kind) params.set("kind", kind);
  if (status) params.set("status", status);
  if (query) params.set("q", query);
  const list = useResource<KnowledgeList>(`knowledge?${params}`, 0);
  const queue = useResource<KnowledgeList>("knowledge?status=pending&limit=100", 0);
  const entries = list.data?.entries ?? [];
  const current = entries.find((entry) => entry.id === selected);

  function reload() {
    list.refresh();
    queue.refresh();
  }

  return <PageShell section="knowledge" className="workspace stack">
    <div className="heading">
      <h1>Knowledge</h1>
      <label className="inline">Brief
        <select value={brief} onChange={(event) => setBrief(event.target.value)}>
          <option value="">All briefs</option>
          {(briefs.data?.briefs ?? []).map((item) => <option key={item.id} value={item.id}>{item.id}</option>)}
        </select>
      </label>
    </div>
    <ResourceState error={briefs.error} retry={briefs.refresh} />
    <form className="actions" role="search" onSubmit={(event) => {
      event.preventDefault();
      setQuery(value(new FormData(event.currentTarget), "q"));
    }}>
      <input name="q" type="search" placeholder="Search title, tags and body" maxLength={200} aria-label="Search knowledge" />
      <button>Search</button>
      {query && <button type="button" className="text-button" onClick={() => setQuery("")}>Clear “{query}”</button>}
    </form>
    <div className="actions" role="group" aria-label="Status">
      {views.map(([id, name]) => <button key={id || "all"} className="text-button" aria-pressed={id === status}
        onClick={() => setStatus(id)}>{name}{id === "pending" && ` (${queue.data?.entries.length ?? 0})`}</button>)}
    </div>
    <div className="actions" role="group" aria-label="Kind">
      <button className="text-button" aria-pressed={kind === ""} onClick={() => setKind("")}>All kinds</button>
      {kinds.map((item) => <button key={item} className="text-button" aria-pressed={item === kind}
        onClick={() => setKind(item)}>{item}</button>)}
    </div>
    <ResourceState error={list.error} loading={!list.data} retry={list.refresh} rows={4} />
    {list.data && !entries.length && <EmptyState>{status === "pending" ? "Nothing waiting for approval." : "No entries match."}</EmptyState>}
    {!!entries.length && <div className="table-wrap" role="region" aria-label="Knowledge entries" tabIndex={0}>
      <table>
        <thead><tr><th>Entry</th><th>Kind</th><th>Brief</th><th>Author</th><th>Status</th><th>Tags</th></tr></thead>
        <tbody>{entries.map((entry) => (
          <tr key={entry.id}>
            <th scope="row"><button className="text-button" aria-pressed={entry.id === selected}
              onClick={() => setSelected(entry.id)}>{entry.title}</button>
              <div className="meta">{entry.id}{entry.pinned && " · pinned"}</div></th>
            <td>{entry.kind}</td>
            <td>{entry.brief_id}</td>
            <td>{entry.author}</td>
            <td>{entry.status}</td>
            <td>{entry.tags.join(", ") || "—"}</td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
    {current && <EntryDetail key={`${current.id}:${current.updated_at}`} entry={current}
      changed={(entry) => { list.setData({ entries: entries.map((item) => item.id === entry.id ? entry : item) }); reload(); }}
      removed={() => { setSelected(""); reload(); }} />}
    {briefs.data && <NewEntry briefs={briefs.data.briefs} created={(entry) => { setSelected(entry.id); reload(); }} />}
  </PageShell>;
}
