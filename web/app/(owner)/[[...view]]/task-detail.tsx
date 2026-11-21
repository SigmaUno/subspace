"use client";

import Link from "next/link";
import type { ReactNode } from "react";
import { api, label, money, safeURL, usePagedResource, useResource, useSubmit, value } from "../../../lib/client";
import type { AuditEvent, TaskDetail, TaskList, TimelineEntry } from "../../../lib/task-detail-types";
import { statuses, type Message } from "../../../lib/types";
import { Feedback, LoadMore } from "./components";
import { useLive } from "./live";
import { PRBadges, usePR } from "./pr-status";

const statusNames = new Map<string, string>(statuses);
const settled = ["done", "cancelled"];
const reopenable = ["needs_owner", "blocked", "cancelled"];
const completable = ["approved", "needs_owner"];

const stateOf = (state: AuditEvent["before"]) => typeof state?.status === "string" ? state.status : undefined;
const statusName = (status: string) => statusNames.get(status) ?? label(status);
const stamp = (time: string | null) => time ? new Date(time).toLocaleString() : "—";

function timeline(events: AuditEvent[], messages: Message[]): TimelineEntry[] {
  const entries: TimelineEntry[] = events.map((event) => {
    const before = stateOf(event.before);
    const after = stateOf(event.after);
    return {
      key: `audit-${event.id}`,
      at: event.ts,
      actor: event.agent_id || event.role,
      title: after && before !== after
        ? `${before ? statusName(before) : "New"} → ${statusName(after)}`
        : label(event.tool),
      detail: event.note ?? undefined,
    };
  });
  for (const message of messages) {
    if (message.kind === "message" || message.kind === "status") continue;
    entries.push({
      key: `message-${message.id}`,
      at: message.created_at,
      actor: message.author_id,
      title: label(message.kind),
      detail: message.body,
    });
  }
  return entries.sort((a, b) => Date.parse(a.at) - Date.parse(b.at) || a.key.localeCompare(b.key));
}

function Fact({ name, children }: { name: string; children: ReactNode }) {
  return <div><div className="meta">{name}</div><div className="break">{children}</div></div>;
}

function Burn({ name, used, max, text }: { name: string; used: number; max: number; text: string }) {
  return <div className="stack">
    <div className="meta">{name}</div>
    <div>{text}</div>
    {max > 0 && <progress aria-label={name} value={Math.min(used, max)} max={max} />}
  </div>;
}

function ActionForm({ title, button, danger, children, run }: {
  title: string; button: string; danger?: boolean; children?: ReactNode;
  run: (fields: FormData) => Promise<void>;
}) {
  const form = useSubmit();
  return <form className="stack" onSubmit={(event) => form.submit(event, async (fields, element) => {
    await run(fields);
    element.reset();
  })}>
    <h3>{title}</h3>
    {children}
    <Feedback error={form.error} />
    <div><button className={danger ? "danger" : "primary"} disabled={form.busy}>{button}</button></div>
  </form>;
}

function OwnerActions({ task, changed }: { task: TaskDetail; changed: (task: TaskDetail) => void }) {
  const path = `tasks/${encodeURIComponent(task.id)}`;
  const transition = async (action: string, body: Record<string, string>) => {
    changed(await api<TaskDetail>(`${path}/${action}`, "POST", body));
  };
  return <section className="panel stack" data-write>
    <h2>Owner actions</h2>
    <div className="form-grid">
      {completable.includes(task.status) && <ActionForm title="Mark done" button="Mark done"
        run={(fields) => transition("done", { note: value(fields, "note") })}>
        <label>Note<input name="note" placeholder="Merged in #123" /></label>
      </ActionForm>}
      {reopenable.includes(task.status) && <ActionForm title="Reopen" button="Reopen"
        run={(fields) => transition("reopen", { status: value(fields, "status"), note: value(fields, "note") })}>
        <label>Status
          <select name="status" defaultValue={task.assignee ? "assigned" : "ready"}>
            <option value="ready">Ready</option>
            <option value="assigned">Assigned</option>
          </select>
        </label>
        <label>Note<input name="note" /></label>
      </ActionForm>}
      <ActionForm title="Set budget" button="Save budget" run={async (fields) => {
        const updated = await api<TaskDetail>(path, "PATCH", {
          budget: { max_minutes: Number(value(fields, "minutes")), max_cost_eur: Number(value(fields, "cost")) },
        });
        changed(updated);
      }}>
        <div className="form-grid" key={`${task.budget.max_minutes}-${task.budget.max_cost_eur}`}>
          <label>Max minutes<input name="minutes" type="number" min="0" step="1" defaultValue={task.budget.max_minutes} required /></label>
          <label>Max cost (EUR)<input name="cost" type="number" min="0" step="0.01" defaultValue={task.budget.max_cost_eur} required /></label>
        </div>
      </ActionForm>
      {!settled.includes(task.status) && <ActionForm title="Cancel task" button="Cancel task" danger
        run={async (fields) => {
          if (!window.confirm(`Cancel ${task.id}?`)) return;
          await transition("cancel", { reason: value(fields, "reason") });
        }}>
        <label>Reason<input name="reason" /></label>
      </ActionForm>}
    </div>
  </section>;
}

export function TaskDetailPage({ id }: { id: string }) {
  const { revisions, connected } = useLive(`task:${id},thread:task:${id}`);
  const path = encodeURIComponent(id);
  const task = useResource<TaskDetail>(`tasks/${path}`, revisions.board);
  const briefID = task.data?.brief_id;
  const siblings = useResource<TaskList>(
    briefID ? `tasks?${new URLSearchParams({ brief_id: briefID, limit: "1000" })}` : null, revisions.board);
  const audit = usePagedResource<AuditEvent>(
    `audit?${new URLSearchParams({ task_id: id })}`, "events", revisions.board + revisions.thread);
  const messages = usePagedResource<Message>(`threads/task/${path}/messages`, "messages", revisions.thread, 200);
  const prStatus = usePR(id, revisions.board);
  const reply = useSubmit();

  const current = task.data;
  const known = new Map(siblings.data?.tasks?.map((item) => [item.id, item]));
  const log = messages.items;
  const entries = timeline(audit.items, log);
  const pr = safeURL(current?.pr_url);

  function changed(updated: TaskDetail) {
    task.setData(updated);
    audit.refresh();
    messages.refresh();
  }

  return <>
    <header className="topbar">
      <Link className="brand" href="/"><span className="brand-mark" aria-hidden="true" />Subspace</Link>
      <nav aria-label="Workspace">
        <Link href="/">Swarm</Link>
        <Link href="/briefs">Briefs</Link>
      </nav>
      <div className="top-actions">
        <span className={`connection ${connected ? "online" : ""}`} role="status">{connected ? "Live" : "Reconnecting"}</span>
      </div>
    </header>
    <main className="workspace">
      <Feedback error={task.error} loading={!current && !task.error} />
      {current && <>
        <div className="heading">
          <div>
            <p className="meta">{current.id} · <Link href={`/briefs/${encodeURIComponent(current.brief_id)}`}>{current.brief_id}</Link> · {current.role}</p>
            <h1>{current.title}</h1>
          </div>
          <span className="pill" data-status={current.status}>{statusName(current.status)}</span>
        </div>
        {current.blocked_reason && <div className="notice" role="status">{label(current.blocked_reason)}</div>}

        <section className="panel stack">
          <h2>Overview</h2>
          <div className="form-grid">
            <Fact name="Assignee">{current.assignee ?? "Unassigned"}</Fact>
            <Fact name="Reviewer">{current.reviewer ?? "None"}</Fact>
            <Fact name="Review rounds">{current.review_rounds}</Fact>
            <Fact name="Repository">{current.repo}</Fact>
            <Fact name="Branch">{current.branch || "—"}</Fact>
            <Fact name="Pull request">{prStatus.data
              ? <PRBadges pr={prStatus.data} />
              : pr ? <a href={pr} target="_blank" rel="noreferrer">Open pull request</a> : "No PR"}</Fact>
            <Fact name="Tests">{current.tests
              ? <>{current.tests.passed} passed · {current.tests.failed} failed{current.tests.reported_at && <> · <time dateTime={current.tests.reported_at}>{stamp(current.tests.reported_at)}</time></>}</>
              : "Not reported"}</Fact>
            <Fact name="Progress">{current.progress_pct}%</Fact>
            <Fact name="Updated"><time dateTime={current.updated_at}>{stamp(current.updated_at)}</time></Fact>
          </div>
          <div className="form-grid">
            <Burn name="Cost" used={current.cost_used} max={current.budget.max_cost_eur}
              text={`${money(current.cost_used)} / ${money(current.budget.max_cost_eur)}`} />
            <Burn name="Minutes" used={current.minutes_used} max={current.budget.max_minutes}
              text={`${current.minutes_used} / ${current.budget.max_minutes} min`} />
          </div>
        </section>

        <section className="panel stack">
          <h2>Specification</h2>
          <p className="preserve">{current.spec || "No specification."}</p>
          <h3>Acceptance criteria</h3>
          {current.acceptance_criteria?.length
            ? <ul>{current.acceptance_criteria.map((item, index) => <li key={index} className="preserve">{item}</li>)}</ul>
            : <p className="empty">No acceptance criteria.</p>}
          <h3>Dependencies</h3>
          {current.depends_on?.length
            ? <ul>{current.depends_on.map((dependency) => {
              const other = known.get(dependency);
              return <li key={dependency}>
                <Link href={`/tasks/${encodeURIComponent(dependency)}`}>{dependency}</Link>
                {other && <> · {other.title} <span className="pill" data-status={other.status}>{statusName(other.status)}</span></>}
              </li>;
            })}</ul>
            : <p className="empty">No dependencies.</p>}
        </section>

        <OwnerActions task={current} changed={changed} />

        <section className="panel stack">
          <h2>Timeline</h2>
          <Feedback error={audit.error} loading={!audit.loaded && !audit.error} />
          {audit.loaded && !entries.length && <p className="empty">No activity yet.</p>}
          <div className="messages">
            {entries.map((entry) => (
              <article key={entry.key}>
                <div className="message-meta"><strong>{entry.title}</strong><span>{entry.actor}</span>
                  <time dateTime={entry.at}>{stamp(entry.at)}</time>
                </div>
                {entry.detail && <p className="preserve">{entry.detail}</p>}
              </article>
            ))}
            <LoadMore hasMore={audit.hasMore} loading={audit.loading} onMore={audit.loadMore} />
          </div>
        </section>

        <section className="panel stack">
          <h2>Thread</h2>
          <Feedback error={messages.error} loading={!messages.loaded && !messages.error} />
          {messages.loaded && !log.length && <p className="empty">No messages yet.</p>}
          <div className="messages" role="log" aria-label="Messages">
            {log.map((message) => (
              <article key={message.id}>
                <div className="message-meta"><strong>{message.author_id}</strong><span>{message.author_role} · {message.kind}</span>
                  <time dateTime={message.created_at}>{stamp(message.created_at)}</time>
                </div>
                <p className="preserve">{message.body}</p>
              </article>
            ))}
            <LoadMore hasMore={messages.hasMore} loading={messages.loading} onMore={messages.loadMore} />
          </div>
          <form className="stack" data-write onSubmit={(event) => reply.submit(event, async (fields, element) => {
            const body = value(fields, "body");
            if (!body) throw new Error("Enter a reply.");
            await api(`threads/task/${path}/messages`, "POST", { body });
            element.reset();
            messages.refresh();
          })}>
            <label>Owner reply<textarea name="body" required rows={3} /></label>
            <Feedback error={reply.error} />
            <div><button className="primary" disabled={reply.busy}>Send reply</button></div>
          </form>
        </section>
      </>}
    </main>
  </>;
}
