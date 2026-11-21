"use client";

import { useEffect, useMemo, useRef, useState, type ReactNode, type UIEvent } from "react";
import { api } from "../../../../lib/client";
import type { AgentOutput, LatestOutput, OutputChunk, OutputLine } from "../../../../lib/output-types";
import { ansiCss, parseAnsi, plainText } from "../../../../lib/ansi";
import { atEnd, freshLines, matchLines, mergeLatest, newest, retain, rowHeight, viewHeight, visibleRange } from "../../../../lib/terminal";
import "./terminal.css";
import { EntityLink } from "../shared/components";
import { EmptyState, ResourceState } from "../shared/states";

const pageSize = 1000;
const tileSize = 12;

function highlight(text: string, query: string): ReactNode {
  const lower = text.toLowerCase();
  if (!query || lower.length !== text.length) return text;
  const parts: ReactNode[] = [];
  let at = 0;
  for (let index = lower.indexOf(query); index >= 0; index = lower.indexOf(query, at)) {
    if (index > at) parts.push(text.slice(at, index));
    parts.push(<mark key={index}>{text.slice(index, index + query.length)}</mark>);
    at = index + query.length;
  }
  if (at < text.length) parts.push(text.slice(at));
  return parts;
}

function Styled({ text, query = "" }: { text: string; query?: string }) {
  return <>{parseAnsi(text).map((segment, index) =>
    <span key={index} style={ansiCss(segment.style)}>{highlight(segment.text, query)}</span>)}</>;
}

function TerminalLine({ line, query }: { line: OutputLine; query: string }) {
  const title = `#${line.seq} · ${new Date(line.ts).toLocaleTimeString()}${line.task_id ? ` · ${line.task_id}` : ""}`;
  return <div className="terminal-line" data-stream={line.stream} title={title}><Styled text={line.text} query={query} /></div>;
}

const message = (cause: unknown) => cause instanceof Error ? cause.message : "Request failed.";

function TerminalView({ agent, taskID }: { agent: string; taskID?: string }) {
  const [lines, setLines] = useState<OutputLine[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [live, setLive] = useState(false);
  const [follow, setFollow] = useState(true);
  const [search, setSearch] = useState("");
  const [scrollTop, setScrollTop] = useState(0);
  const [copied, setCopied] = useState(false);
  const box = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const controller = new AbortController();
    const pending: OutputLine[] = [];
    let last = 0;
    let frame: number | undefined;
    let stream: EventSource | undefined;
    const base = `agents/${encodeURIComponent(agent)}/output`;

    const paint = () => {
      frame = undefined;
      const batch = pending.splice(0);
      setLines((current) => retain(current, batch));
    };
    const add = (incoming: OutputLine[]) => {
      const fresh = freshLines(incoming, last, taskID);
      if (!fresh.length) return;
      last = fresh[fresh.length - 1].seq;
      pending.push(...fresh);
      if (frame === undefined) frame = requestAnimationFrame(paint);
    };
    const fail = (cause: unknown) => { if (!controller.signal.aborted) setError(message(cause)); };
    const load = (since: number): Promise<void> => {
      const params = new URLSearchParams({ limit: String(pageSize) });
      if (since > 0) params.set("since", String(since));
      if (taskID) params.set("task_id", taskID);
      return api<AgentOutput>(`${base}?${params}`, "GET", undefined, controller.signal).then((out) => {
        if (since > 0 && out.gap) return load(0);
        if (since === 0) {
          last = 0;
          pending.length = 0;
          setLines([]);
        }
        add(out.lines);
        setError("");
        if (since > 0 && out.more) return load(out.next_since);
      });
    };

    load(0).then(() => {
      if (controller.signal.aborted) return;
      setLoaded(true);
      stream = new EventSource(`/api/v1/events?${new URLSearchParams({ topics: `output:${agent}` })}`);
      const catchUp = () => { load(last).catch(fail); };
      stream.addEventListener("hello", () => { setLive(true); catchUp(); });
      stream.addEventListener("resync", catchUp);
      stream.addEventListener("lagged", catchUp);
      stream.addEventListener("agent.output", (event) => {
        try {
          const { data } = JSON.parse((event as MessageEvent<string>).data) as { data?: OutputChunk };
          if (data?.agent_id === agent && Array.isArray(data.lines)) add(data.lines);
        } catch {}
      });
      stream.onerror = () => setLive(false);
    }).catch(fail);

    return () => {
      controller.abort();
      stream?.close();
      if (frame !== undefined) cancelAnimationFrame(frame);
    };
  }, [agent, taskID]);

  const query = search.trim().toLowerCase();
  const shown = useMemo(() => matchLines(lines, query), [lines, query]);

  useEffect(() => {
    const element = box.current;
    if (follow && element) element.scrollTop = element.scrollHeight;
  }, [shown, follow]);

  function onScroll(event: UIEvent<HTMLDivElement>) {
    const element = event.currentTarget;
    setScrollTop(element.scrollTop);
    const bottom = atEnd(element.scrollHeight, element.scrollTop, element.clientHeight);
    if (bottom !== follow) setFollow(bottom);
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(shown.map((line) => plainText(line.text)).join("\n"));
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      setError("Could not copy to the clipboard.");
    }
  }

  const { start, end } = visibleRange(scrollTop, shown.length);

  return <section className="panel stack" aria-label={`Live output of ${agent}`}>
    <div className="heading">
      <h2>Live output{taskID && <span className="meta">{agent}</span>}</h2>
      <div className="terminal-actions">
        <span className={`connection ${live ? "online" : ""}`} role="status">{live ? "Live" : "Connecting"}</span>
        <label className="terminal-search">Search<input type="search" value={search} onChange={(event) => setSearch(event.target.value)} /></label>
        <button type="button" aria-pressed={!follow} onClick={() => setFollow(!follow)}>{follow ? "Pause" : "Follow"}</button>
        <button type="button" onClick={copy} disabled={!shown.length}>{copied ? "Copied" : "Copy"}</button>
      </div>
    </div>
    <ResourceState error={error} loading={!loaded && !error} />
    {loaded && !lines.length && <EmptyState>No output yet.</EmptyState>}
    {!!lines.length && <div ref={box} className="terminal" role="log" aria-label={`Output of ${agent}`} tabIndex={0}
      onScroll={onScroll} style={{ height: viewHeight }}>
      <div className="terminal-range" style={{ height: shown.length * rowHeight }}>
        <div style={{ transform: `translateY(${start * rowHeight}px)` }}>
          {shown.slice(start, end).map((line) => <TerminalLine key={line.seq} line={line} query={query} />)}
        </div>
      </div>
    </div>}
    {!!query && <p className="meta">{shown.length} matching of {lines.length} lines</p>}
    {!follow && !!lines.length && <p className="meta">Auto-scroll paused. Scroll to the end or press Follow to resume.</p>}
  </section>;
}

export function AgentTerminal({ agent, taskID }: { agent: string; taskID?: string }) {
  return <TerminalView key={`${agent}\u0000${taskID ?? ""}`} agent={agent} taskID={taskID} />;
}

export function OutputTile() {
  const [agents, setAgents] = useState<LatestOutput[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    const controller = new AbortController();
    const pending = new Map<string, LatestOutput>();
    let frame: number | undefined;
    const load = () => {
      api<{ agents: LatestOutput[] | null }>("agent-output", "GET", undefined, controller.signal)
        .then((out) => { setAgents((out.agents ?? []).sort(newest)); setLoaded(true); setError(""); })
        .catch((cause) => { if (!controller.signal.aborted) setError(message(cause)); });
    };
    const paint = () => {
      frame = undefined;
      const updates = [...pending.values()];
      pending.clear();
      setAgents((current) => mergeLatest(current, updates));
    };
    load();
    const timer = setInterval(load, 30_000);
    const stream = new EventSource(`/api/v1/events?${new URLSearchParams({ topics: "output" })}`);
    stream.addEventListener("agent.output", (event) => {
      try {
        const { data } = JSON.parse((event as MessageEvent<string>).data) as { data?: OutputChunk };
        if (!data || !Array.isArray(data.lines) || !data.lines.length) return;
        const line = data.lines[data.lines.length - 1];
        pending.set(data.agent_id, { agent_id: data.agent_id, line, lines: data.lines.length, updated_at: line.ts });
        if (frame === undefined) frame = requestAnimationFrame(paint);
      } catch {}
    });
    return () => {
      controller.abort();
      clearInterval(timer);
      stream.close();
      if (frame !== undefined) cancelAnimationFrame(frame);
    };
  }, []);

  return <section className="panel stack" aria-label="Agent activity">
    <div className="heading"><h2>Agent activity</h2><span className="meta">Latest output line per active agent</span></div>
    <ResourceState error={error} loading={!loaded && !error} />
    {loaded && !agents.length && <EmptyState>No agent output yet.</EmptyState>}
    {!!agents.length && <ul className="terminal-tile">
      {agents.slice(0, tileSize).map((entry) => <li key={entry.agent_id}>
        <EntityLink kind="agent" id={entry.agent_id} />
        <time dateTime={entry.updated_at} className="meta">{new Date(entry.updated_at).toLocaleTimeString()}</time>
        <code className="terminal-line" data-stream={entry.line.stream}><Styled text={entry.line.text} /></code>
      </li>)}
    </ul>}
  </section>;
}
