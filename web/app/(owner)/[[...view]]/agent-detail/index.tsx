"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { api, label, money, safeURL, usePagedResource, useResource, useSubmit, value } from "../../../../lib/client";
import type { AuditEvent } from "../../../../lib/agent-detail-types";
import type { Agent, Connect, Message, Registration, Task } from "../../../../lib/types";
import type { UsageSummary } from "../../../../lib/usage-types";
import { AgentQuotaPanel } from "./quota";
import { Clock, Feedback, Heartbeat, LoadMore } from "../shared/components";
import { InsightsPanel } from "../insights";
import { useLive } from "../shared/live";
import { CopyBlock } from "../shared/copy";
import { AgentTerminal } from "../terminal";
import { PageShell } from "../shell";
import { useConfirm, useToast } from "../shared/ui";
import { count, stamp, usd } from "../shared/format";
import { EmptyState, ResourceState } from "../shared/states";

const dayMs = 86_400_000;
const days = 30;
const finished = ["done", "cancelled"];


function ConnectPanel({ connect, rotated, dismiss }: { connect: Connect; rotated: boolean; dismiss: () => void }) {
  return <section className="panel stack">
    <div className="heading"><h2>Connect</h2>{rotated && <button onClick={dismiss}>Dismiss token</button>}</div>
    {rotated && <p className="notice">Save this token now. It is displayed once and the previous token no longer works.</p>}
    {rotated && <CopyBlock label="Agent token" text={connect.token} />}
    <CopyBlock label="Join line" text={connect.join_line} />
    <CopyBlock label="MCP URL" text={connect.mcp_url} />
    <CopyBlock label="Claude Code" text={connect.snippets.claude_code} />
    <CopyBlock label="Codex config.toml" text={connect.snippets.codex_toml} />
    <CopyBlock label="OpenCode / DeepSeek opencode.json" text={connect.snippets.opencode_json} />
  </section>;
}

function AgentThread({ id, revision }: { id: string; revision: number }) {
  const path = `threads/agent/${encodeURIComponent(id)}/messages`;
  const messages = usePagedResource<Message>(path, "messages", revision, 200);
  const form = useSubmit();
  const items = messages.items;
  return <section className="panel stack">
    <h2>Thread</h2>
    <ResourceState error={messages.error} loading={!messages.loaded} retry={messages.refresh} />
    {messages.loaded && !items.length && <EmptyState>No messages yet.</EmptyState>}
    {!!items.length && <div className="messages" role="log" aria-label="Agent thread" tabIndex={0}>
      {items.map((message) => <article key={message.id}>
        <div className="message-meta"><strong>{message.author_id}</strong><span>{message.author_role} · {message.kind}</span>
          <time dateTime={message.created_at}>{stamp(message.created_at)}</time></div>
        <p className="preserve">{message.body}</p>
      </article>)}
      <LoadMore hasMore={messages.hasMore} loading={messages.loading} onMore={messages.loadMore} />
    </div>}
    <form className="stack" data-write onSubmit={(event) => form.submit(event, async (fields, element) => {
      const body = value(fields, "body");
      if (!body) throw new Error("Enter a reply.");
      await api<Message>(path, "POST", { body });
      element.reset();
      messages.refresh();
    })}>
      <label>Owner reply<textarea name="body" required rows={3} /></label>
      <Feedback error={form.error} />
      <div><button className="primary" disabled={form.busy}>Send reply</button></div>
    </form>
  </section>;
}

function TaskTable({ tasks, empty }: { tasks: Task[]; empty: string }) {
  if (!tasks.length) return <EmptyState>{empty}</EmptyState>;
  return <div className="table-wrap">
    <table>
      <thead><tr><th>Task</th><th>Status</th><th>Progress</th><th>Branch</th><th>Spent</th></tr></thead>
      <tbody>{tasks.map((task) => {
        const pr = safeURL(task.pr_url);
        return <tr key={task.id}>
          <th scope="row">{task.title}<div className="meta">{task.id} · {task.repo}</div></th>
          <td><span className="pill" data-status={task.status}>{label(task.status)}</span>
            {task.blocked_reason && <div className="meta">{task.blocked_reason}</div>}</td>
          <td>{task.progress_pct}%</td>
          <td>{task.branch || "—"}{pr && <div className="meta"><a href={pr} target="_blank" rel="noreferrer">Pull request</a></div>}</td>
          <td>{money(task.cost_used)}<div className="meta">{task.minutes_used} min</div></td>
        </tr>;
      })}</tbody>
    </table>
  </div>;
}

function Tasks({ id, revision }: { id: string; revision: number }) {
  const tasks = useResource<{ tasks: Task[] | null }>(`tasks?${new URLSearchParams({ assignee: id, limit: "1000" })}`, revision);
  const all = tasks.data?.tasks ?? [];
  return <>
    <section className="stack">
      <h2>Current tasks</h2>
      <ResourceState error={tasks.error} loading={!tasks.data} retry={tasks.refresh} />
      {tasks.data && <TaskTable tasks={all.filter((task) => !finished.includes(task.status))} empty="No current tasks." />}
    </section>
    <section className="stack">
      <h2>Past tasks</h2>
      {tasks.data && <TaskTable tasks={all.filter((task) => finished.includes(task.status))} empty="No finished tasks." />}
    </section>
  </>;
}

function Usage({ id, now, revision }: { id: string; now: number; revision: number }) {
  const to = (Math.floor(now / dayMs) + 1) * dayMs;
  const params = new URLSearchParams({ from: new Date(to - days * dayMs).toISOString(), to: new Date(to).toISOString(), group_by: "agent" });
  const summary = useResource<UsageSummary>(now ? `usage/summary?${params}` : null, revision);
  const group = summary.data?.groups?.find((entry) => entry.key === id);
  return <section className="panel stack">
    <div className="heading"><h2>Usage</h2><span className="meta">Last {days} days</span></div>
    <ResourceState error={summary.error} loading={!summary.data} retry={summary.refresh} />
    {summary.data && !group && <EmptyState>No usage recorded in this period.</EmptyState>}
    {group && <div className="table-wrap" role="region" aria-label="Usage" tabIndex={0}>
      <table>
        <thead><tr><th>Cost</th><th>Requests</th><th>Tokens in</th><th>Tokens out</th><th>Cached</th><th>Rate limited</th></tr></thead>
        <tbody><tr>
          <td>{usd.format(group.cost_usd)}</td>
          <td>{count.format(group.requests)}</td>
          <td>{count.format(group.tokens_in)}</td>
          <td>{count.format(group.tokens_out)}</td>
          <td>{count.format(group.cache_read)}</td>
          <td className={group.rate_limited ? "stale" : undefined}>{group.rate_limited}</td>
        </tr></tbody>
      </table>
    </div>}
  </section>;
}

const summarize = (state: Record<string, unknown> | null) =>
  state ? Object.entries(state).map(([key, entry]) => `${key}: ${typeof entry === "string" ? entry : JSON.stringify(entry)}`).join(", ") : "—";

function AuditTrail({ id, revision }: { id: string; revision: number }) {
  const audit = usePagedResource<AuditEvent>(`audit?${new URLSearchParams({ agent_id: id })}`, "events", revision);
  const events = audit.items;
  return <section className="stack">
    <h2>Audit trail</h2>
    <ResourceState error={audit.error} loading={!audit.loaded} retry={audit.refresh} />
    {audit.loaded && !events.length && <EmptyState>No audit events.</EmptyState>}
    {!!events.length && <div className="table-wrap" role="region" aria-label="Audit events" tabIndex={0}>
      <table>
        <thead><tr><th>Time</th><th>Tool</th><th>Task</th><th>Before</th><th>After</th><th>Note</th></tr></thead>
        <tbody>{events.map((event) => <tr key={event.id}>
          <td><time dateTime={event.ts}>{stamp(event.ts)}</time></td>
          <th scope="row">{event.tool}</th>
          <td>{event.task_id ?? "—"}</td>
          <td>{summarize(event.before)}</td>
          <td>{summarize(event.after)}</td>
          <td>{event.note ?? "—"}</td>
        </tr>)}</tbody>
      </table>
    </div>}
    <LoadMore hasMore={audit.hasMore} loading={audit.loading} onMore={audit.loadMore} />
  </section>;
}

function Actions({ agent, changed, rotated }: { agent: Agent; changed: (agent: Agent) => void; rotated: (registration: Registration) => void }) {
  const resume = useSubmit();
  const rotate = useSubmit();
  const revoke = useSubmit();
  const confirm = useConfirm();
  const toast = useToast();
  const id = encodeURIComponent(agent.id);
  const revoked = agent.status === "revoked";
  return <section className="panel stack">
    <h2>Actions</h2>
    {revoked && <p className="meta">This agent is revoked. Its token no longer works.</p>}
    <div className="actions">
      {agent.status === "paused" && <form data-write onSubmit={(event) => resume.submit(event, async () => {
        changed(await api<Agent>(`agents/${id}/resume`, "POST", {}));
        toast(`Resumed ${agent.id}.`);
      })}><button className="primary" disabled={resume.busy}>Resume agent</button></form>}
      {!revoked && <form data-write onSubmit={(event) => rotate.submit(event, async () => {
        if (!await confirm({
          title: `Rotate the token for ${agent.id}?`, message: "The current token stops working immediately.", confirm: "Rotate",
        })) return;
        const result = await api<Registration>(`agents/${id}/rotate-token`, "POST", {});
        changed(result.agent);
        rotated(result);
        toast(`Rotated the token of ${agent.id}.`);
      })}><button disabled={rotate.busy}>Rotate token</button></form>}
    </div>
    <Feedback error={resume.error || rotate.error} />
    {!revoked && <form className="stack" data-write onSubmit={(event) => revoke.submit(event, async (fields) => {
      if (!await confirm({
        title: `Revoke ${agent.id}?`, message: "Its token dies immediately and its leases are released.", confirm: "Revoke",
      })) return;
      changed(await api<Agent>(`agents/${id}/revoke`, "POST", { reason: value(fields, "reason") }));
      toast(`Revoked ${agent.id}.`);
    })}>
      <label>Revocation reason<input name="reason" /></label>
      <Feedback error={revoke.error} />
      <div><button className="danger" disabled={revoke.busy}>Revoke agent</button></div>
    </form>}
  </section>;
}

export function AgentDetailPage({ id }: { id: string }) {
  const { revisions } = useLive("all");
  const [now, setNow] = useState(0);
  useEffect(() => {
    const initial = setTimeout(() => setNow(Date.now()), 0);
    const interval = setInterval(() => setNow(Date.now()), 15_000);
    return () => { clearTimeout(initial); clearInterval(interval); };
  }, []);
  const [rotated, setRotated] = useState<Connect | null>(null);
  const path = encodeURIComponent(id);
  const agent = useResource<Agent>(`agents/${path}`, revisions.agents);
  const connect = useResource<Connect>(`agents/${path}/connect`, revisions.agents);
  const current = agent.data;

  return <Clock.Provider value={now}>
    <PageShell section="agents">
      <div className="heading">
        <div><div className="meta"><Link href="/agents">Agents</Link></div><h1>{id}</h1></div>
        {current && <span className={`pill ${current.status}`}>{current.status}</span>}
      </div>
      <ResourceState error={agent.error} loading={!current && !agent.error} retry={agent.refresh} />
      {current && <>
        <section className="panel stack">
          <h2>Agent</h2>
          <dl className="stack">
            <div><dt className="meta">Role</dt><dd>{current.role}</dd></div>
            <div><dt className="meta">Kind / model</dt><dd>{current.kind} · {current.model} · {current.provider}</dd></div>
            <div><dt className="meta">Current task</dt><dd>{current.current_task_id ?? "—"} · {current.assigned_count} assigned</dd></div>
            <div><dt className="meta">Last seen</dt><dd><Heartbeat time={current.last_seen_at} watch={current.status === "active"} /></dd></div>
            {current.pause_reason && <div><dt className="meta">Pause reason</dt><dd>{current.pause_reason}</dd></div>}
          </dl>
        </section>
        <Actions agent={current} changed={(next) => { agent.setData(next); agent.refresh(); }}
          rotated={(result) => { setRotated(result.connect); connect.refresh(); }} />
        {(rotated ?? connect.data) && <ConnectPanel connect={(rotated ?? connect.data)!} rotated={!!rotated} dismiss={() => setRotated(null)} />}
        <Feedback error={connect.error} />
        <AgentQuotaPanel id={id} revision={revisions.agents + revisions.board} revoked={current.status === "revoked"} />
        <InsightsPanel id={id} revision={revisions.agents + revisions.board + revisions.thread} />
        <AgentTerminal agent={id} />
        <AgentThread id={id} revision={revisions.thread} />
        <Tasks id={id} revision={revisions.board} />
        <Usage id={id} now={now} revision={revisions.capacity} />
        <AuditTrail id={id} revision={revisions.board + revisions.agents} />
      </>}
    </PageShell>
  </Clock.Provider>;
}
