"use client";

import Link from "next/link";
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { api, optionalResource, safeURL, useResource, useSubmit, value } from "../../../lib/client";
import type { Decision } from "../../../lib/swarm";
import type { Thread } from "../../../lib/types";
import { Feedback } from "./components";

export type DecisionFeed = { decisions: Decision[]; count?: number };

const labels: Record<Decision["kind"], string> = {
  plan: "Plan approval",
  question: "Question",
  merge: "Merge approval",
  unblock: "Unblock",
};

function DecisionCard({ item, acting, next, openThread }: {
  item: Decision;
  acting: (id: string) => void;
  next: (id: string) => void;
  openThread: (thread: Thread) => void;
}) {
  const { busy, error, submit } = useSubmit();
  const form = useRef<HTMLFormElement>(null);
  const response = useRef<HTMLTextAreaElement>(null);
  const rejectButton = useRef<HTMLButtonElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const [text, setText] = useState("");
  const question = item.kind === "question";
  const unblock = item.kind === "unblock";
  const primary = question ? "Answer" : unblock ? "Unblock" : "Approve";
  const allowReject = !question && !unblock;
  const pullRequest = safeURL(item.pr_url);
  const pr = pullRequest ? new URL(pullRequest) : null;
  if (pr && /\/pulls?\/\d+\/?$/.test(pr.pathname)) pr.pathname = pr.pathname.replace(/\/$/, "") + "/files";
  const thread = useMemo<Thread | null>(() => item.task_id ? { kind: "task", id: item.task_id }
    : item.agent_id ? { kind: "agent", id: item.agent_id }
    : item.brief_id ? { kind: "brief", id: item.brief_id } : null, [item.task_id, item.agent_id, item.brief_id]);

  useEffect(() => { heading.current?.focus({ preventScroll: true }); }, []);

  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      const target = event.target;
      if (event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey || event.repeat ||
        (target instanceof HTMLElement && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName)))) return;
      if (busy || !(target instanceof HTMLElement) || !form.current?.closest("aside")?.contains(target)) return;
      const key = event.key.toLowerCase();
      if (key === "a") {
        event.preventDefault();
        if (question) response.current?.focus();
        else form.current?.requestSubmit();
      }
      if (key === "r" && allowReject) {
        event.preventDefault();
        if (text.trim() && rejectButton.current) form.current?.requestSubmit(rejectButton.current);
        else response.current?.focus();
      }
      if (key === "s") { event.preventDefault(); next(item.id); }
      if (key === "t" && thread) { event.preventDefault(); openThread(thread); }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [allowReject, busy, item.id, next, openThread, question, text, thread]);

  function decide(event: FormEvent<HTMLFormElement>) {
    const button = (event.nativeEvent as SubmitEvent).submitter;
    const reject = button instanceof HTMLButtonElement && button.value === "reject";
    void submit(event, async (fields) => {
      const detail = value(fields, "response");
      if ((question || reject) && !detail) throw new Error(question ? "Enter an answer." : "Enter a rejection reason.");
      const action = question ? "answer" : unblock ? "unblock" : reject ? "reject" : "approve";
      await api(`decisions/${encodeURIComponent(item.id)}`, "POST", {
        action,
        ...(question ? { answer: detail } : { note: detail }),
      });
      acting(item.id);
    });
  }

  return <article className="stack">
    <div className="meta">{labels[item.kind] ?? item.kind}{item.brief_id && ` · ${item.brief_id}`}{item.task_id && ` · ${item.task_id}`}</div>
    <h3 tabIndex={-1} ref={heading}>{item.title}</h3>
    {item.body && <p className="preserve">{item.body}</p>}
    <div className="actions">
      {thread && <button type="button" onClick={() => openThread(thread)}>Open thread <kbd>T</kbd></button>}
      {pr && <a href={pr.href} target="_blank" rel="noreferrer">PR diff</a>}
    </div>
    {!!item.options?.length && <div className="actions">{item.options.map((option) =>
      <button type="button" key={option} onClick={() => { setText(option); response.current?.focus(); }}>{option}</button>)}</div>}
    <form className="stack" ref={form} onSubmit={decide}>
      <label>{question ? "Answer" : allowReject ? "Note or rejection reason" : "Note"}
        <textarea name="response" rows={2} ref={response} value={text}
          onChange={(event) => setText(event.target.value)} required={question} />
      </label>
      <Feedback error={error} />
      <div className="actions">
        <button type="submit" className="primary" disabled={busy}>{primary} <kbd>A</kbd></button>
        {allowReject && <button type="submit" className="danger" value="reject" ref={rejectButton} disabled={busy}>Reject <kbd>R</kbd></button>}
        <button type="button" onClick={() => next(item.id)} disabled={busy}>Skip <kbd>S</kbd></button>
      </div>
    </form>
  </article>;
}

export function DecisionInbox({ feed, count, revision, refresh, close, openThread, error }: {
  feed?: DecisionFeed | null;
  count: number;
  revision: number;
  refresh: () => void;
  close: () => void;
  openThread: (thread: Thread) => void;
  error: string;
}) {
  const next = useResource("decisions/next", revision, optionalResource<{ decision: Decision | null }>);
  const [skipped, setSkipped] = useState<string[]>([]);
  const [resolved, setResolved] = useState<string[]>([]);
  const [selected, setSelected] = useState<string>();
  const queue = (feed?.decisions ?? [])
    .filter((item) => !resolved.includes(item.id))
    .sort((a, b) => (b.impact ?? 0) - (a.impact ?? 0) || a.created_at.localeCompare(b.created_at));
  const serverNext = next.data?.decision;
  const eligible = queue.filter((item) => !skipped.includes(item.id));
  const item = eligible.find((item) => item.id === selected) ??
    (serverNext && !resolved.includes(serverNext.id) && !skipped.includes(serverNext.id) ? serverNext : eligible[0]);
  if (item?.id !== selected) setSelected(item?.id);

  return <aside className="panel stack decision-panel" aria-label="Decision inbox">
    <div className="heading"><h2>Decision inbox <span className="pill">{count}</span></h2><button onClick={close}>Close</button></div>
    <Feedback error={error || next.error} />
    {item && <DecisionCard key={item.id} item={item} openThread={openThread}
      next={(id) => setSkipped((items) => [...items, id])}
      acting={(id) => { setResolved((items) => [...items, id]); refresh(); next.refresh(); }} />}
    {!item && feed === undefined && !error && <Feedback loading />}
    {!item && feed === null && <div className="actions"><Link href="/plans">Plan approvals</Link><Link href="/questions">Questions</Link></div>}
    {!item && feed && <p className="empty">{skipped.length ? "No unskipped decisions." : "No pending decisions."}</p>}
    {skipped.length > 0 && <div><button type="button" onClick={() => setSkipped([])}>Review skipped</button></div>}
  </aside>;
}
