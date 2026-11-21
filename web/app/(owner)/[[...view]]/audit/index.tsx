"use client";

import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { api, optionalResource, useResource, value } from "../../../../lib/client";
import type { AuditEntry, AuditFilters, AuditList, AuditVerify } from "../../../../lib/audit-types";
import { useLive } from "../shared/live";
import { VirtualList } from "../shared/virtual";
import { PageShell } from "../shell";
import { stamp } from "../shared/format";
import { EmptyState, ResourceState } from "../shared/states";

const pageSize = 1000;
const maxPages = 10;
const none: AuditFilters = { agent_id: "", task_id: "", tool: "", since: "" };
const failure = (cause: unknown) => cause instanceof Error ? cause.message : "Request failed.";

function filterQuery(filters: AuditFilters) {
  const query = new URLSearchParams();
  (["agent_id", "task_id", "tool"] as const).forEach((key) => { if (filters[key]) query.set(key, filters[key]); });
  return query;
}

async function fetchRows(filters: AuditFilters, from: number, signal: AbortSignal) {
  const rows: AuditEntry[] = [];
  let cursor = from;
  let more = false;
  for (let page = 0; page < maxPages; page++) {
    const query = filterQuery(filters);
    query.set("since", String(cursor));
    query.set("limit", String(pageSize));
    const batch = (await api<AuditList>(`audit?${query}`, "GET", undefined, signal)).events ?? [];
    rows.push(...batch);
    more = batch.length === pageSize;
    if (!more) break;
    cursor = batch[batch.length - 1].id;
  }
  return { rows, more };
}

function download(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.click();
  URL.revokeObjectURL(url);
}

function ChainBadge() {
  const verify = useResource("audit/verify", 0, optionalResource<AuditVerify>);
  if (verify.error) return <span className="pill" data-status="blocked" title={verify.error}>Chain unavailable</span>;
  if (verify.data === undefined) return <span className="pill">Chain checking</span>;
  if (verify.data === null) return null;
  const intact = verify.data.ok ?? verify.data.valid ?? ["ok", "valid", "verified"].includes(verify.data.status ?? "");
  const detail = verify.data.error ?? (verify.data.broken_at ? `Broken at event ${verify.data.broken_at}` : undefined);
  return <span className="pill" data-status={intact ? "done" : "blocked"} title={detail}>
    {intact ? "Chain intact" : "Chain broken"}
  </span>;
}

function Json({ title, data }: { title: string; data: unknown }) {
  return <div>
    <div className="meta">{title}</div>
    {data == null ? <p className="muted">—</p> : <pre>{JSON.stringify(data, null, 2)}</pre>}
  </div>;
}

function AuditRow({ entry, expanded, toggle }: { entry: AuditEntry; expanded: boolean; toggle: () => void }) {
  return <div className="agent-row">
    <div>
      <button className="text-button" aria-expanded={expanded} onClick={toggle}>#{entry.id}</button>
      <div className="meta"><time dateTime={entry.ts}>{stamp(entry.ts)}</time></div>
    </div>
    <div>{entry.agent_id ?? "—"}<div className="meta">{entry.role ?? "—"}</div></div>
    <div>{entry.tool}<div className="meta">{entry.task_id ?? "—"}</div></div>
    <div className="ellipsis" title={entry.note ?? undefined}>{entry.note ?? "—"}</div>
    {expanded && <div className="stack" style={{ gridColumn: "1 / -1" }}>
      <Json title="Before" data={entry.before} />
      <Json title="After" data={entry.after} />
    </div>}
  </div>;
}

export function AuditPage() {
  const { revisions } = useLive("board");
  const [applied, setApplied] = useState(none);
  const [loaded, setLoaded] = useState<{ filters: AuditFilters; rows: AuditEntry[]; more: boolean }>();
  const [error, setError] = useState("");
  const [live, setLive] = useState(false);
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(new Set());
  const [busy, setBusy] = useState(false);
  const cursor = useRef(0);
  const ready = loaded?.filters === applied;

  function append(rows: AuditEntry[], more: boolean, filters: AuditFilters) {
    setLoaded((current) => {
      if (!current || current.filters !== filters) return current;
      const last = current.rows.at(-1)?.id ?? 0;
      const fresh = rows.filter((row) => row.id > last);
      cursor.current = Math.max(cursor.current, fresh.at(-1)?.id ?? 0);
      return { ...current, rows: [...current.rows, ...fresh], more };
    });
  }

  useEffect(() => {
    const controller = new AbortController();
    const from = Number(applied.since) || 0;
    fetchRows(applied, from, controller.signal)
      .then(({ rows, more }) => {
        cursor.current = rows.at(-1)?.id ?? from;
        setLoaded({ filters: applied, rows, more });
        setError("");
      })
      .catch((cause) => { if (!controller.signal.aborted) setError(failure(cause)); });
    return () => controller.abort();
  }, [applied]);

  useEffect(() => {
    if (!live || !ready) return;
    const controller = new AbortController();
    fetchRows(applied, cursor.current, controller.signal)
      .then(({ rows, more }) => { if (rows.length) append(rows, more, applied); })
      .catch((cause) => { if (!controller.signal.aborted) setError(failure(cause)); });
    return () => controller.abort();
  }, [live, ready, applied, revisions.board]);

  const rows = loaded?.rows;
  const items = useMemo(() => [...(rows ?? [])].reverse(), [rows]);

  function apply(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    setApplied({ agent_id: value(form, "agent_id"), task_id: value(form, "task_id"), tool: value(form, "tool"), since: value(form, "since") });
  }

  function reset(form: HTMLFormElement) {
    form.reset();
    setApplied(none);
  }

  async function loadMore() {
    if (!loaded) return;
    setError("");
    try {
      const { rows: next, more } = await fetchRows(applied, cursor.current, new AbortController().signal);
      append(next, more, applied);
    } catch (cause) {
      setError(failure(cause));
    }
  }

  async function exportRows() {
    if (!loaded) return;
    setBusy(true);
    setError("");
    try {
      const response = await fetch(`/api/v1/audit/export?${filterQuery(applied)}`, {
        headers: { "X-Subspace-Request": "1" },
        cache: "no-store",
      });
      if (response.status === 401) window.location.replace("/login");
      if (response.ok) {
        download(await response.blob(), "audit.ndjson");
      } else if ([404, 501].includes(response.status)) {
        const lines = loaded.rows.map((row) => `${JSON.stringify(row)}\n`);
        download(new Blob(lines, { type: "application/x-ndjson" }), "audit.ndjson");
      } else {
        throw new Error(`Export failed (HTTP ${response.status}).`);
      }
    } catch (cause) {
      setError(failure(cause));
    } finally {
      setBusy(false);
    }
  }

  function toggle(id: number) {
    setExpanded((current) => {
      const next = new Set(current);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  }

  return <PageShell section="audit">
    <div className="heading">
      <h1>Audit log</h1>
      <div className="actions">
        <ChainBadge />
        <label className="inline">
          <input type="checkbox" checked={live} onChange={(event) => setLive(event.target.checked)} />
          Live tail
        </label>
        <button onClick={exportRows} disabled={busy || !ready}>Export NDJSON</button>
      </div>
    </div>

    <form className="panel stack" onSubmit={apply}>
      <div className="form-grid">
        <label>Agent<input name="agent_id" /></label>
        <label>Task<input name="task_id" placeholder="T-1" /></label>
        <label>Tool<input name="tool" placeholder="claim_task" /></label>
        <label>Since event id<input name="since" type="number" min="0" inputMode="numeric" /></label>
      </div>
      <div className="actions">
        <button className="primary">Apply</button>
        <button type="button" onClick={(event) => reset(event.currentTarget.form as HTMLFormElement)}>Reset</button>
      </div>
    </form>

    <ResourceState error={error} loading={!ready && !error} />
    {ready && !items.length && <EmptyState>No audit events.</EmptyState>}
    {!!items.length && <section className="stack">
      <p className="meta" role="status">{items.length} events loaded, newest first.</p>
      <VirtualList name="Audit events" items={items} estimate={72} className="agent-list"
        render={(entry) => <AuditRow entry={entry} expanded={expanded.has(entry.id)} toggle={() => toggle(entry.id)} />} />
      {loaded?.more && <div className="actions"><button onClick={loadMore}>Load newer</button></div>}
    </section>}
  </PageShell>;
}
