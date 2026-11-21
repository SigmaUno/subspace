"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { api, useResource, useSubmit, value } from "../../../lib/client";
import { logLevels, type LogLevel, type LogLevelInfo, type LogLine, type LogList } from "../../../lib/logs-types";
import { Feedback } from "./components";
import { VirtualList } from "./virtual";
import { PageShell } from "./shell";
import { EmptyState, ResourceState } from "./states";

const pollInterval = 2_000;
const initialLines = 500;
const maxLines = 2000;
const pillStatus: Record<LogLevel, string> = { debug: "backlog", info: "ready", warn: "changes_requested", error: "blocked" };
const stamp = (time: string) => new Date(time).toLocaleTimeString([], { hour12: false });
const failure = (cause: unknown) => cause instanceof Error ? cause.message : "Request failed.";
const isLevel = (level: string): level is LogLevel => (logLevels as readonly string[]).includes(level);

function summary(line: LogLine) {
  return Object.entries(line.attrs ?? {}).map(([key, val]) => `${key}=${val}`).join(" ");
}

function matches(line: LogLine, text: string) {
  return !text || line.msg.toLowerCase().includes(text) || summary(line).toLowerCase().includes(text);
}

function LevelControl() {
  const current = useResource<LogLevelInfo>("log-level", 0);
  const form = useSubmit();
  return <form className="actions" data-write onSubmit={(event) => form.submit(event, async (fields) => {
    current.setData(await api<LogLevelInfo>("log-level", "PUT", { level: value(fields, "level") }));
  })}>
    <label className="inline">
      Server level
      <select name="level" key={current.data?.level} defaultValue={current.data?.level ?? "info"}>
        {logLevels.map((level) => <option key={level}>{level}</option>)}
      </select>
    </label>
    <button disabled={form.busy || !current.data}>Set</button>
    <Feedback error={form.error || current.error} />
  </form>;
}

function LogRow({ line, expanded, toggle }: { line: LogLine; expanded: boolean; toggle: () => void }) {
  const attrs = line.attrs ?? {};
  return <div className="agent-row">
    <div>
      <button className="text-button" aria-expanded={expanded} onClick={toggle}>#{line.id}</button>
      <div className="meta"><time dateTime={line.ts}>{stamp(line.ts)}</time></div>
    </div>
    <div><span className="pill" data-status={pillStatus[line.level]}>{line.level}</span></div>
    <div className="ellipsis" title={line.msg}>{line.msg}</div>
    <div className="ellipsis" title={summary(line)}>{attrs.route ? `${attrs.method ?? ""} ${attrs.route} ${attrs.status ?? ""}`.trim() : summary(line) || "—"}</div>
    {expanded && <pre style={{ gridColumn: "1 / -1" }}>{JSON.stringify(attrs, null, 2)}</pre>}
  </div>;
}

export function LogsPage() {
  const [level, setLevel] = useState<LogLevel>("info");
  const [text, setText] = useState("");
  const [live, setLive] = useState(true);
  const [loaded, setLoaded] = useState<{ level: LogLevel; lines: LogLine[] }>();
  const [error, setError] = useState("");
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(new Set());
  const cursor = useRef(0);
  const ready = loaded?.level === level;

  useEffect(() => {
    const controller = new AbortController();
    const query = new URLSearchParams({ level, limit: String(initialLines) });
    api<LogList>(`logs?${query}`, "GET", undefined, controller.signal)
      .then(({ lines }) => {
        const rows = lines ?? [];
        cursor.current = rows.at(-1)?.id ?? 0;
        setLoaded({ level, lines: rows });
        setError("");
      })
      .catch((cause) => { if (!controller.signal.aborted) setError(failure(cause)); });
    return () => controller.abort();
  }, [level]);

  useEffect(() => {
    if (!live || !ready) return;
    let controller = new AbortController();
    const timer = window.setInterval(() => {
      controller.abort();
      controller = new AbortController();
      const { signal } = controller;
      const query = new URLSearchParams({ level, since: String(cursor.current) });
      api<LogList>(`logs?${query}`, "GET", undefined, signal)
        .then(({ lines }) => {
          const fresh = (lines ?? []).filter((line) => line.id > cursor.current);
          if (!fresh.length) return;
          cursor.current = fresh[fresh.length - 1].id;
          setLoaded((current) => current?.level === level
            ? { level, lines: [...current.lines, ...fresh].slice(-maxLines) }
            : current);
          setError("");
        })
        .catch((cause) => { if (!signal.aborted) setError(failure(cause)); });
    }, pollInterval);
    return () => {
      window.clearInterval(timer);
      controller.abort();
    };
  }, [live, ready, level]);

  const lines = loaded?.lines;
  const needle = text.trim().toLowerCase();
  const items = useMemo(() => (lines ?? []).filter((line) => matches(line, needle)).reverse(), [lines, needle]);

  function toggle(id: number) {
    setExpanded((current) => {
      const next = new Set(current);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  }

  return <PageShell section="logs">
    <div className="heading">
      <h1>Server logs</h1>
      <div className="actions">
        <label className="inline">
          <input type="checkbox" checked={live} onChange={(event) => setLive(event.target.checked)} />
          Live tail
        </label>
        <button onClick={() => setLoaded({ level, lines: [] })} disabled={!ready}>Clear</button>
      </div>
    </div>

    <section className="panel stack">
      <div className="form-grid">
        <label>Minimum level
          <select value={level} onChange={(event) => { if (isLevel(event.target.value)) setLevel(event.target.value); }}>
            {logLevels.map((name) => <option key={name}>{name}</option>)}
          </select>
        </label>
        <label>Contains<input value={text} onChange={(event) => setText(event.target.value)} placeholder="route, request id, agent" /></label>
      </div>
      <LevelControl />
    </section>

    <ResourceState error={error} loading={!ready && !error} />
    {ready && !items.length && <EmptyState>No log lines.</EmptyState>}
    {!!items.length && <section className="stack">
      <p className="meta" role="status">{items.length} lines, newest first.</p>
      <VirtualList name="Log lines" items={items} estimate={64} className="agent-list"
        render={(line) => <LogRow line={line} expanded={expanded.has(line.id)} toggle={() => toggle(line.id)} />} />
    </section>}
  </PageShell>;
}
