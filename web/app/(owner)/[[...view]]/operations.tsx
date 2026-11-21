"use client";

import Link from "next/link";
import { useState } from "react";
import { api, useResource, useSubmit, value } from "../../../lib/client";
import type { Agent, Capacity, CapacityLimit, Plan, Question, Registration, Thread } from "../../../lib/types";
import { agentActions, BulkBar, BulkToast, SelectAll, SelectBox, useBulk } from "./bulk";
import { Feedback, Heartbeat } from "./components";
import { PlanEditor } from "./plan-edit";
import { VirtualList } from "./virtual";

function RegistrationForm({ complete }: { complete: (items: Registration[]) => void }) {
  const [bulk, setBulk] = useState(false);
  const form = useSubmit();
  return (
    <form className="panel stack" onSubmit={(event) => form.submit(event, async (fields) => {
      const common = { role: value(fields, "role"), kind: value(fields, "kind"),
        model: value(fields, "model"), provider: value(fields, "provider") };
      const id = value(fields, "id");
      if (bulk) {
        const result = await api<{ agents: Registration[] }>("agents/bulk", "POST", {
          ...common, prefix: id, count: Number(value(fields, "count")),
        });
        complete(result.agents);
      } else {
        complete([await api<Registration>("agents", "POST", { ...common, id })]);
      }
    })}>
      <h2>Register agents</h2>
      <label className="inline"><input type="checkbox" checked={bulk} onChange={(event) => setBulk(event.target.checked)} /> Bulk registration</label>
      <div className="form-grid">
        <label>{bulk ? "Agent ID prefix" : "Agent ID"}
          <input name="id" pattern={"[a-z0-9][a-z0-9_\\-]*"} maxLength={bulk ? 58 : 63} required />
        </label>
        {bulk && <label>Count<input name="count" type="number" min="1" max="1000" defaultValue="2" required /></label>}
        <label>Role<select name="role" defaultValue="worker">
          <option value="leader">Leader</option><option value="worker">Worker</option>
          <option value="tester">Tester</option><option value="reviewer">Reviewer</option>
        </select></label>
        <label>Kind<select name="kind" defaultValue="codex">
          <option value="claude-code">Claude Code</option><option value="codex">Codex</option>
          <option value="opencode">OpenCode</option><option value="other">Other</option>
        </select></label>
        <label>Model<input name="model" placeholder="deepseek/deepseek-chat" required /></label>
        <label>Provider override<input name="provider" placeholder="Derived from kind" /></label>
      </div>
      <Feedback error={form.error} />
      <div><button className="primary" disabled={form.busy}>Register {bulk ? "agents" : "agent"}</button></div>
    </form>
  );
}

export function Secret({ label, value }: { label: string; value: string }) {
  const [feedback, setFeedback] = useState("");
  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setFeedback("Copied");
    } catch {
      setFeedback("Select and copy the text.");
    }
  }
  return <div className="secret">
    <div className="heading"><strong>{label}</strong>
      <button type="button" onClick={copy} aria-label={`Copy ${label}`}>Copy</button>
    </div>
    <pre>{value}</pre>
    {feedback && <span className="meta" role="status">{feedback}</span>}
  </div>;
}

function RegistrationResults({ items, dismiss }: { items: Registration[]; dismiss: () => void }) {
  return <section className="panel stack">
    <div className="heading"><h2>Agent credentials</h2><button onClick={dismiss}>Dismiss credentials</button></div>
    <p className="notice">Save these credentials now. Tokens are displayed once.</p>
    {items.map(({ agent, connect }, index) => (
      <details key={agent.id} open={index === 0}>
        <summary>{agent.id}</summary>
        <Secret label="Agent token" value={connect.token} />
        <Secret label="Join line" value={connect.join_line} />
        <Secret label="Claude Code" value={connect.snippets.claude_code} />
        <Secret label="Codex config.toml" value={connect.snippets.codex_toml} />
        <Secret label="OpenCode / DeepSeek opencode.json" value={connect.snippets.opencode_json} />
      </details>
    ))}
  </section>;
}

export function AgentsView({ revision, openThread }: { revision: number; openThread: (thread: Thread) => void }) {
  const agents = useResource<{ agents: Agent[] }>("agents?limit=1000", revision);
  const [showForm, setShowForm] = useState(false);
  const [registered, setRegistered] = useState<Registration[] | null>(null);
  const bulk = useBulk();
  return <>
    <div className="heading"><h1>Agents</h1>
      <button className="primary" disabled={!!registered} onClick={() => setShowForm(!showForm)}>{showForm ? "Close form" : "Register agents"}</button></div>
    {showForm && <RegistrationForm complete={(items) => { setRegistered(items); setShowForm(false); agents.refresh(); }} />}
    {registered && <RegistrationResults items={registered} dismiss={() => setRegistered(null)} />}
    <Feedback error={agents.error} loading={!agents.data} />
    {agents.data && !agents.data.agents?.length && <p className="empty">No agents registered.</p>}
    {!!agents.data?.agents?.length && <div className="table-wrap">
      <div className="registry-table">
        <div className="registry-row selectable registry-head"><SelectAll ids={agents.data.agents.map((agent) => agent.id)} bulk={bulk} name="agents" /><span>Agent</span><span>Role</span><span>Kind / model</span><span>Status</span><span>Current task</span><span>Load</span><span>Last heartbeat</span></div>
        <VirtualList items={agents.data.agents} name="Registered agents" estimate={58} className="registry-list" render={(agent) => (
          <article className="registry-row selectable">
            <SelectBox id={agent.id} bulk={bulk} name={agent.id} />
            <div><Link href={`/agents/${encodeURIComponent(agent.id)}`}>{agent.id}</Link>
              <div><button className="text-button meta" onClick={() => openThread({ kind: "agent", id: agent.id })}>Thread</button></div></div>
            <div>{agent.role}</div><div>{agent.kind}<div className="meta">{agent.model} · {agent.provider}</div></div>
            <div><span className={`pill ${agent.status}`}>{agent.status}</span>{agent.pause_reason && <div className="meta">{agent.pause_reason}</div>}</div>
            <div>{agent.current_task_id ? <button className="text-button" onClick={() => openThread({ kind: "task", id: agent.current_task_id! })}>{agent.current_task_id}</button> : "—"}</div>
            <div>{agent.assigned_count} assigned</div><div><Heartbeat time={agent.last_seen_at} watch={agent.status === "active"} /></div>
          </article>
        )} />
      </div>
    </div>}
    <BulkBar kind="agents" bulk={bulk} actions={agentActions} />
    <BulkToast bulk={bulk} />
  </>;
}

function PlanCard({ plan, decided, changed, openThread }: { plan: Plan; decided: (id: string) => void; changed: () => void; openThread: (thread: Thread) => void }) {
  const form = useSubmit();
  const count = plan.tasks?.filter((task) => task.status !== "cancelled").length ?? 0;
  return <article className="panel stack">
    <div className="heading">
      <div><h2>Plan {plan.id}</h2><p className="meta">Proposed by {plan.proposed_by} · <Link href={`/briefs/${encodeURIComponent(plan.brief_id)}`}>{plan.brief_id}</Link></p></div>
      <div className="actions"><span className="pill">{count} tasks</span><button onClick={() => openThread({ kind: "brief", id: plan.brief_id })}>Brief thread</button></div>
    </div>
    {plan.note && <p className="preserve">{plan.note}</p>}
    <PlanEditor plan={plan} changed={changed} />
    <form className="stack" onSubmit={(event) => {
      const button = (event.nativeEvent as SubmitEvent).submitter;
      const reject = button instanceof HTMLButtonElement && button.value === "reject";
      void form.submit(event, async (fields) => {
        const note = value(fields, "note");
        if (reject && !note) throw new Error("Enter a reason to reject this plan.");
        await api<Plan>(`plans/${encodeURIComponent(plan.id)}/${reject ? "reject" : "approve"}`, "POST",
          reject ? { reason: note } : { note });
        decided(plan.id);
      });
    }}>
      <label>Decision note or rejection reason<textarea name="note" rows={2} /></label>
      <Feedback error={form.error} />
      <div className="actions"><button className="primary" disabled={form.busy || !count}>Approve plan</button>
        <button className="danger" type="submit" value="reject" disabled={form.busy}>Reject plan</button></div>
    </form>
  </article>;
}

export function PlansView({ revision, openThread }: { revision: number; openThread: (thread: Thread) => void }) {
  const plans = useResource<{ plans: Plan[] }>("plans?status=proposed&limit=1000", revision);
  const [decided, setDecided] = useState<string[]>([]);
  const items = plans.data?.plans?.filter((plan) => !decided.includes(plan.id)) ?? [];
  return <>
    <div className="heading"><h1>Plan approvals</h1></div>
    <Feedback error={plans.error} loading={!plans.data} />
    {plans.data && !items.length && <p className="empty">No plans awaiting approval.</p>}
    {items.map((plan) => <PlanCard key={plan.id} plan={plan} openThread={openThread} changed={plans.refresh} decided={(id) => {
      setDecided((current) => [...current, id]);
      plans.refresh();
    }} />)}
  </>;
}

function QuestionCard({ question, answered, openThread }: { question: Question; answered: (id: string) => void; openThread: (thread: Thread) => void }) {
  const form = useSubmit();
  const [answer, setAnswer] = useState("");
  return <article className="panel stack">
    <div className="meta">
      {question.id} · <button className="text-button" onClick={() => openThread({ kind: "agent", id: question.agent_id })}>{question.agent_id}</button>
      {question.task_id && <> · <button className="text-button" onClick={() => openThread({ kind: "task", id: question.task_id! })}>{question.task_id}</button></>}
      {question.brief_id && <> · <Link href={`/briefs/${encodeURIComponent(question.brief_id)}`}>{question.brief_id}</Link></>}
    </div>
    <h2 className="preserve">{question.question}</h2>
    {!!question.options?.length && <div className="actions">
      {question.options.map((option, index) => <button key={index} onClick={() => setAnswer(option)}>{option}</button>)}
    </div>}
    <form className="stack" onSubmit={(event) => form.submit(event, async (fields) => {
      const text = value(fields, "answer");
      if (!text) throw new Error("Enter an answer.");
      await api<Question>(`questions/${encodeURIComponent(question.id)}/answer`, "POST", { answer: text });
      answered(question.id);
    })}>
      <label>Answer<textarea name="answer" value={answer} onChange={(event) => setAnswer(event.target.value)} required rows={2} /></label>
      <Feedback error={form.error} />
      <div><button className="primary" disabled={form.busy}>Send answer</button></div>
    </form>
  </article>;
}

export function QuestionsView({ revision, openThread }: { revision: number; openThread: (thread: Thread) => void }) {
  const questions = useResource<{ questions: Question[] }>("questions?status=open&limit=1000", revision);
  const [answered, setAnswered] = useState<string[]>([]);
  const items = questions.data?.questions?.filter((question) => !answered.includes(question.id)) ?? [];
  return <>
    <div className="heading"><h1>Questions inbox</h1></div>
    <Feedback error={questions.error} loading={!questions.data} />
    {questions.data && !items.length && <p className="empty">No open questions.</p>}
    {items.map((question) => <QuestionCard key={question.id} question={question} openThread={openThread} answered={(id) => {
      setAnswered((current) => [...current, id]);
      questions.refresh();
    }} />)}
  </>;
}

type LimitInput = Pick<CapacityLimit, "provider" | "model" | "max_concurrency" | "requests_per_min">;
type Draft = { global_max_agents: number; limits: LimitInput[] };

const whole = (amount: number) => Number.isInteger(amount) && amount >= 0;

function capacityProblem(draft: Draft): string | undefined {
  if (!whole(draft.global_max_agents)) return "Global maximum agents must be a whole number of 0 or more.";
  for (const [index, limit] of draft.limits.entries()) {
    if (!limit.provider.trim() || !limit.model.trim()) return `Limit ${index + 1}: provider and model are required (use "*" for any model).`;
    if (!whole(limit.max_concurrency) || !whole(limit.requests_per_min)) return `Limit ${index + 1}: concurrency and requests per minute must be whole numbers of 0 or more.`;
  }
}

export function CapacityView({ revision }: { revision: number }) {
  const capacity = useResource<Capacity>("capacity", revision);
  const [draft, setDraft] = useState<Draft>();
  const [saved, setSaved] = useState(false);
  const form = useSubmit();
  const current = capacity.data;
  const settings: Draft | undefined = draft ?? (current && {
    global_max_agents: current.global_max_agents,
    limits: current.limits ?? [],
  });

  function update(next: Draft) {
    setDraft(next);
    setSaved(false);
  }

  return <>
    <div className="heading"><h1>Capacity settings</h1>{current && <span className="pill">{current.agents_in_use} agents in use</span>}</div>
    <Feedback error={capacity.error} loading={!current} />
    {settings && <form className="panel stack" onSubmit={(event) => form.submit(event, async () => {
      const problem = capacityProblem(settings);
      if (problem) throw new Error(problem);
      const response = await api<Capacity>("capacity", "PUT", {
        global_max_agents: settings.global_max_agents,
        limits: settings.limits.map(({ provider, model, max_concurrency, requests_per_min }) => ({
          provider: provider.trim(), model: model.trim(), max_concurrency, requests_per_min,
        })),
      });
      capacity.setData(response);
      capacity.refresh();
      setDraft(undefined);
      setSaved(true);
    })}>
      <fieldset className="stack" disabled={form.busy}>
        <label className="limit-global">Global maximum agents
          <input type="number" min="0" required value={settings.global_max_agents}
            onChange={(event) => update({ ...settings, global_max_agents: event.target.valueAsNumber })} />
        </label>
        <h2>Provider and model limits</h2>
        {!settings.limits.length && <p className="muted">No provider or model limits configured.</p>}
        {settings.limits.map((limit, index) => {
          const usage = current?.limits?.find((row) => row.provider === limit.provider && row.model === limit.model);
          function edit(changes: Partial<LimitInput>) {
            update({ ...settings!, limits: settings!.limits.map((row, i) => i === index ? { ...row, ...changes } : row) });
          }
          return <div className="capacity-row" key={index}>
            <label>Provider<input required value={limit.provider} onChange={(event) => edit({ provider: event.target.value })} /></label>
            <label>Model or *<input required value={limit.model} onChange={(event) => edit({ model: event.target.value })} /></label>
            <label>Max concurrent<input type="number" min="0" required value={limit.max_concurrency} onChange={(event) => edit({ max_concurrency: event.target.valueAsNumber })} /></label>
            <label>Requests / min<input type="number" min="0" required value={limit.requests_per_min} onChange={(event) => edit({ requests_per_min: event.target.valueAsNumber })} /></label>
            <span className="meta">{usage ? `${usage.in_use} in use · ${usage.available} free` : ""}</span>
            <button type="button" onClick={() => update({ ...settings, limits: settings.limits.filter((_, i) => i !== index) })}>Remove</button>
          </div>;
        })}
        <div className="actions">
          <button type="button" onClick={() => update({ ...settings, limits: [...settings.limits, { provider: "", model: "*", max_concurrency: 1, requests_per_min: 0 }] })}>Add limit</button>
          {draft && <button type="button" onClick={() => setDraft(undefined)}>Discard changes</button>}
          <button className="primary">Save settings</button>
        </div>
      </fieldset>
      <Feedback error={form.error} />
      {saved && <span className="meta" role="status">Capacity settings saved.</span>}
    </form>}
  </>;
}
