"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useState } from "react";
import { api, label, safeURL, useResource } from "../../../lib/client";
import type { Decision, DecisionOutcome, DecisionPayload } from "../../../lib/decisions-types";
import type { Question } from "../../../lib/types";
import { Feedback } from "./components";
import { useLive } from "./live";

const oldAfter = 60 * 60_000;
const snoozes = [["15m", "15 minutes"], ["1h", "1 hour"], ["4h", "4 hours"], ["24h", "1 day"], ["168h", "7 days"]];
const nav = [
  ["briefs", "Briefs"], ["agents", "Agents"], ["plans", "Plans"], ["questions", "Questions"],
  ["decisions", "Decisions"], ["capacity", "Capacity"],
];

function age(ms: number) {
  const minutes = Math.floor(ms / 60_000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return hours < 48 ? `${hours}h` : `${Math.floor(hours / 24)}d`;
}

function typing(target: EventTarget | null) {
  return target instanceof HTMLElement && !!target.closest("input, textarea, select, [contenteditable]");
}

type Act = (id: string, payload: DecisionPayload) => Promise<void>;

function DecisionCard({ decision, selected, options, busy, now, select, act }: {
  decision: Decision; selected: boolean; options: string[]; busy: boolean; now: number;
  select: () => void; act: Act;
}) {
  const [note, setNote] = useState("");
  const [answer, setAnswer] = useState("");
  const [status, setStatus] = useState<"" | "ready" | "assigned">("");
  const [snooze, setSnooze] = useState("1h");
  const elapsed = now ? now - Date.parse(decision.created_at) : 0;
  const old = decision.kind === "stale_agent" || elapsed >= oldAfter;
  const has = (action: string) => decision.actions.some((candidate) => candidate === action);
  const pr = safeURL(decision.links.pr ?? null);
  const briefHref = decision.brief_id ? `/briefs/${decision.brief_id}` : undefined;

  return (
    <article id={`decision-${decision.id}`} className={`panel decision${selected ? " selected" : ""}${old ? " old" : ""}`}
      aria-current={selected ? "true" : undefined}>
      <div className="heading">
        <h2><button className="text-button" onClick={select}>{decision.title}</button></h2>
        <span className="pill">{label(decision.kind)}</span>
      </div>
      <p className="preserve">{decision.summary}</p>
      <div className="decision-meta meta">
        <span title="Priority">Priority {decision.priority}</span>
        <time className={old ? "stale" : undefined} dateTime={decision.created_at} title={decision.created_at}>
          {elapsed ? `${age(elapsed)} old` : new Date(decision.created_at).toLocaleString()}{old && " · stale"}
        </time>
        {decision.kind === "plan" && <Link href="/plans">Plan {decision.id.slice(decision.id.indexOf(":") + 1)}</Link>}
        {decision.task_id && (briefHref ? <Link href={briefHref}>Task {decision.task_id}</Link> : <span>Task {decision.task_id}</span>)}
        {decision.agent_id && <Link href="/agents">Agent {decision.agent_id}</Link>}
        {briefHref && <Link href={briefHref}>Brief {decision.brief_id}</Link>}
        {pr && <a href={pr} target="_blank" rel="noreferrer">Pull request</a>}
      </div>
      {selected && (
        <div className="decision-actions">
          {has("answer") && (
            <form className="stack" onSubmit={(event) => {
              event.preventDefault();
              if (answer.trim()) void act(decision.id, { action: "answer", answer: answer.trim() });
            }}>
              {options.length > 0 && (
                <div className="actions" role="group" aria-label="Options">
                  {options.map((option) => (
                    <button type="button" key={option} onClick={() => setAnswer(option)}>{option}</button>
                  ))}
                </div>
              )}
              <label>Answer
                <textarea rows={3} value={answer} onChange={(event) => setAnswer(event.target.value)} required />
              </label>
              <div><button className="primary" disabled={busy || !answer.trim()}>Answer</button></div>
            </form>
          )}
          {(has("approve") || has("reject") || has("merge") || has("unblock")) && (
            <div className="stack">
              {has("unblock") && (
                <label>Return task to
                  <select value={status} onChange={(event) => setStatus(event.target.value as typeof status)}>
                    <option value="">Default</option>
                    <option value="ready">Ready</option>
                    <option value="assigned">Assigned</option>
                  </select>
                </label>
              )}
              <label>Note
                <input value={note} onChange={(event) => setNote(event.target.value)} />
              </label>
              <div className="actions">
                {has("approve") && <button className="primary" disabled={busy} onClick={() => act(decision.id, { action: "approve", note })}>Approve</button>}
                {has("merge") && <button className="primary" disabled={busy} onClick={() => act(decision.id, { action: "merge", note })}>Merge</button>}
                {has("unblock") && <button className="primary" disabled={busy} onClick={() => act(decision.id, status ? { action: "unblock", note, status } : { action: "unblock", note })}>Unblock</button>}
                {has("reject") && <button className="danger" disabled={busy} onClick={() => act(decision.id, { action: "reject", reason: note })}>Reject</button>}
              </div>
            </div>
          )}
          {has("skip") && (
            <div className="actions">
              <label className="inline">Snooze
                <select value={snooze} onChange={(event) => setSnooze(event.target.value)}>
                  {snoozes.map(([value, name]) => <option key={value} value={value}>{name}</option>)}
                </select>
              </label>
              <button disabled={busy} onClick={() => act(decision.id, { action: "skip", snooze })}>Skip</button>
            </div>
          )}
        </div>
      )}
    </article>
  );
}

export function Decisions() {
  const { revisions, connected } = useLive("questions,plans,board");
  const revision = revisions.board + revisions.questions + revisions.plans;
  const list = useResource<{ decisions: Decision[] | null }>("decisions", revision);
  const next = useResource<{ decision: Decision | null }>("decisions/next", revision);
  const questions = useResource<{ questions: Question[] | null }>("questions?status=open", revision);
  const [picked, setPicked] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [now, setNow] = useState(0);
  const { setData: setList, refresh: refreshList } = list;
  const { setData: setNext, refresh: refreshNext } = next;

  useEffect(() => {
    const initial = setTimeout(() => setNow(Date.now()), 0);
    const interval = setInterval(() => setNow(Date.now()), 15_000);
    return () => { clearTimeout(initial); clearInterval(interval); };
  }, []);

  const decisions = useMemo(() => list.data?.decisions ?? [], [list.data]);
  const current = decisions.find((item) => item.id === picked)
    ?? decisions.find((item) => item.id === next.data?.decision?.id)
    ?? decisions[0];
  const currentID = current?.id;

  const act = useCallback<Act>(async (id, payload) => {
    setBusy(true);
    setError("");
    try {
      const outcome = await api<DecisionOutcome>(`decisions/${encodeURIComponent(id)}`, "POST", payload);
      setList((data) => data && { decisions: (data.decisions ?? []).filter((item) => item.id !== outcome.id) });
      setNext({ decision: outcome.next });
      setPicked(outcome.next?.id ?? null);
      refreshList();
      refreshNext();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Decision request failed.");
    } finally {
      setBusy(false);
    }
  }, [setList, setNext, refreshList, refreshNext]);

  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.metaKey || event.ctrlKey || event.altKey || event.repeat || typing(event.target)) return;
      const index = decisions.findIndex((item) => item.id === currentID);
      if (event.key === "j") setPicked(decisions[Math.min(index + 1, decisions.length - 1)]?.id ?? null);
      else if (event.key === "k") setPicked(decisions[Math.max(index - 1, 0)]?.id ?? null);
      else if (event.key === "n") setPicked(next.data?.decision?.id ?? decisions[0]?.id ?? null);
      else if (event.key === "a" && !busy && decisions[index]?.actions.includes("approve")) {
        void act(decisions[index].id, { action: "approve", note: "" });
      } else if (event.key === "s" && !busy && decisions[index]) {
        void act(decisions[index].id, { action: "skip", snooze: "1h" });
      } else return;
      event.preventDefault();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [decisions, currentID, next.data, busy, act]);

  useEffect(() => {
    if (currentID) document.getElementById(`decision-${currentID}`)?.scrollIntoView({ block: "nearest" });
  }, [currentID]);

  const options = new Map((questions.data?.questions ?? []).map((item) => [`q:${item.id}`, item.options ?? []]));

  return (
    <>
      <header className="topbar">
        <Link className="brand" href="/briefs"><span className="brand-mark" aria-hidden="true" />Subspace</Link>
        <nav aria-label="Workspace">
          {nav.map(([id, name]) => (
            <Link key={id} href={`/${id}`} aria-current={id === "decisions" ? "page" : undefined}>{name}</Link>
          ))}
        </nav>
        <div className="top-actions">
          <span className={`connection ${connected ? "online" : ""}`} role="status">{connected ? "Live" : "Reconnecting"}</span>
        </div>
      </header>
      <main className="workspace">
        <div className="heading">
          <h1>Decisions</h1>
          <span className="meta">j/k move · n next · a approve · s skip</span>
        </div>
        <Feedback error={error || list.error} loading={!list.data && !list.error} />
        {list.data && decisions.length === 0 && <p className="empty">Nothing needs a decision.</p>}
        <div className="decision-list">
          {decisions.map((item) => (
            <DecisionCard key={item.id} decision={item} selected={item.id === currentID} options={options.get(item.id) ?? []}
              busy={busy} now={now} select={() => setPicked(item.id)} act={act} />
          ))}
        </div>
      </main>
    </>
  );
}
