"use client";

import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../../../lib/client";
import type { TranscriptLine, TranscriptPage } from "../../../lib/transcript-types";
import { localTimeToRFC3339, transcriptPath, transcriptSize, type TranscriptQuery } from "../../../lib/transcript";
import { Feedback, LoadMore } from "./components";
import { EmptyState } from "./states";
import { Styled } from "./terminal";
import "./terminal.css";
import "./transcript.css";

const message = (cause: unknown) => cause instanceof Error ? cause.message : "Request failed.";
const clock = (ts: string) => new Date(ts).toLocaleTimeString();

export function TaskTranscript({ taskID, revision }: { taskID: string; revision: number }) {
  const [filter, setFilter] = useState<TranscriptQuery>({});
  const [text, setText] = useState("");
  const [local, setLocal] = useState("");
  const [lines, setLines] = useState<TranscriptLine[]>([]);
  const [page, setPage] = useState<TranscriptPage>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => {
    const controller = new AbortController();
    api<TranscriptPage>(transcriptPath(taskID, filter), "GET", undefined, controller.signal)
      .then((first) => {
        setLines(first.lines ?? []);
        setPage(first);
        setError("");
      })
      .catch((cause: unknown) => { if (!controller.signal.aborted) setError(message(cause)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [taskID, filter, revision]);

  const more = useCallback(async () => {
    if (!page?.more || loading) return;
    setLoading(true);
    try {
      const next = await api<TranscriptPage>(transcriptPath(taskID, { ...filter, since: page.next_since }));
      setLines((current) => [...current, ...(next.lines ?? [])]);
      setPage(next);
      setError("");
    } catch (cause) {
      setError(message(cause));
    } finally {
      setLoading(false);
    }
  }, [page, loading, taskID, filter]);

  function apply(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (local && !localTimeToRFC3339(local)) {
      setError("Enter a valid date and time.");
      return;
    }
    setLoading(true);
    setFilter({ q: text, at: localTimeToRFC3339(local) });
  }

  function clear() {
    setText("");
    setLocal("");
    setLoading(true);
    setFilter({});
  }

  const query = (filter.q ?? "").trim().toLowerCase();
  const downloadable = (page?.segments ?? 0) > 0;
  return <section className="panel stack" aria-label="Transcript">
    <div className="heading">
      <h2>Transcript</h2>
      {downloadable && <a href={`/api/v1/tasks/${encodeURIComponent(taskID)}/transcript/download`} download={`${taskID}.ndjson.gz`}>
        Download .ndjson.gz{page ? ` (${transcriptSize(page.bytes)})` : ""}
      </a>}
    </div>
    <form className="transcript-tools" onSubmit={apply}>
      <label>Search<input type="search" value={text} onChange={(event) => setText(event.target.value)} maxLength={200} /></label>
      <label>Jump to time<input type="datetime-local" step="1" value={local} onChange={(event) => setLocal(event.target.value)} /></label>
      <button className="primary" disabled={loading}>Apply</button>
      <button type="button" onClick={clear} disabled={loading || (!filter.q && !filter.at && !text && !local)}>Clear</button>
    </form>
    <Feedback error={error} />
    {page?.gap && <p className="notice" role="status">The oldest lines were dropped by the size limit or archived by retention.</p>}
    {!loading && !error && !lines.length && <EmptyState>{query || filter.at ? "No lines match." : "No output was recorded for this task."}</EmptyState>}
    {lines.length > 0 && <div className="terminal transcript-box" role="log" aria-label="Transcript lines" tabIndex={0}>
      {lines.map((line) => <div key={line.offset} className="terminal-line transcript-line" data-stream={line.stream}
        title={`${line.agent_id} · ${line.stream}`}>
        <span className="transcript-meta">#{line.offset}</span>
        <span className="transcript-meta"><time dateTime={line.ts}>{clock(line.ts)}</time></span>
        <span><Styled text={line.text} query={query} /></span>
      </div>)}
    </div>}
    <LoadMore hasMore={Boolean(page?.more)} loading={loading} onMore={more} />
  </section>;
}
