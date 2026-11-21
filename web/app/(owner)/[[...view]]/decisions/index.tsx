"use client";

import Link from "next/link";
import { useEffect, useId, useMemo, useRef, useState, type FormEvent } from "react";
import { api, optionalResource, useResource, useSubmit, value } from "../../../../lib/client";
import { decisionThread, diffURL, orderDecisions, pickDecision } from "../../../../lib/decision-queue";
import type { Decision } from "../../../../lib/swarm";
import type { Thread } from "../../../../lib/types";
import { Feedback } from "../shared/components";
import { DecisionGates } from "../gates";
import { EmptyState, ResourceState, Skeleton } from "../shared/states";

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
  const formID = useId();
  const form = useRef<HTMLFormElement>(null);
  const response = useRef<HTMLTextAreaElement>(null);
  const rejectButton = useRef<HTMLButtonElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const [text, setText] = useState("");
  const question = item.kind === "question";
  const unblock = item.kind === "unblock";
  const merge = item.kind === "merge" ? (item.actions?.includes("merge") ? "merge" : "mark_done") : undefined;
  const primary = question ? "Answer" : unblock ? "Unblock" : merge === "merge" ? "Enqueue merge" : merge ? "Mark merged" : "Approve";
  const allowReject = !question && !unblock && !merge;
  const pr = diffURL(item.pr_url);
  const thread = useMemo(() => decisionThread({ task_id: item.task_id, agent_id: item.agent_id, brief_id: item.brief_id }),
    [item.task_id, item.agent_id, item.brief_id]);

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
      const action = question ? "answer" : unblock ? "unblock" : reject ? "reject" : merge ?? "approve";
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
    {item.task_id && ["merge", "merge-ready"].includes(String(item.kind)) && <DecisionGates taskID={item.task_id} />}
    <div className="actions">
      {thread && <button type="button" onClick={() => openThread(thread)}>Open thread <kbd>T</kbd></button>}
      {pr && <a href={pr.href} target="_blank" rel="noreferrer">PR diff</a>}
    </div>
    {!!item.options?.length && <div className="actions">{item.options.map((option) =>
      <button type="button" key={option} onClick={() => { setText(option); response.current?.focus(); }}>{option}</button>)}</div>}
    <form id={formID} className="stack" data-write ref={form} onSubmit={decide}>
      <label>{question ? "Answer" : allowReject ? "Note or rejection reason" : "Note"}
        <textarea name="response" rows={2} ref={response} value={text}
          onChange={(event) => setText(event.target.value)} required={question} />
      </label>
      <Feedback error={error} />
    </form>
    <div className="actions decision-bar" data-write>
      <button type="submit" form={formID} className="primary" disabled={busy}>{primary} <kbd>A</kbd></button>
      {allowReject && <button type="submit" form={formID} className="danger" value="reject" ref={rejectButton} disabled={busy}>Reject <kbd>R</kbd></button>}
      <button type="button" onClick={() => next(item.id)} disabled={busy}>Skip <kbd>S</kbd></button>
    </div>
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
  const queue = orderDecisions(feed?.decisions ?? [], resolved);
  const serverNext = next.data?.decision;
  const eligible = queue.filter((item) => !skipped.includes(item.id));
  const item = pickDecision(eligible, selected, serverNext, resolved, skipped);
  if (item?.id !== selected) setSelected(item?.id);

  return <aside className="panel stack decision-panel" aria-label="Decision inbox">
    <div className="heading"><h2>Decision inbox <span className="pill">{count}</span></h2><button onClick={close}>Close</button></div>
    <ResourceState error={error || next.error} retry={() => { refresh(); next.refresh(); }} rows={3} />
    {item && <DecisionCard key={item.id} item={item} openThread={openThread}
      next={(id) => setSkipped((items) => [...items, id])}
      acting={(id) => { setResolved((items) => [...items, id]); refresh(); next.refresh(); }} />}
    {!item && feed === undefined && !error && <Skeleton rows={3} />}
    {!item && feed === null && <div className="actions"><Link href="/plans">Plan approvals</Link><Link href="/questions">Questions</Link></div>}
    {!item && feed && <EmptyState>{skipped.length ? "No unskipped decisions." : "No pending decisions."}</EmptyState>}
    {skipped.length > 0 && <div><button type="button" onClick={() => setSkipped([])}>Review skipped</button></div>}
  </aside>;
}
