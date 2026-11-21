"use client";

import Link from "next/link";
import { useEffect, useState, useSyncExternalStore, type ReactNode } from "react";
import { api, commaList, useResource, useSubmit, value } from "../../../lib/client";
import type { Agent, Brief, Plan, Registration } from "../../../lib/types";
import type { Charter, OnboardingMemory, OnboardingProgress, OnboardingState, OnboardingStep } from "../../../lib/onboarding-types";
import { Feedback } from "./components";
import type { Revisions } from "./live";
import { Secret } from "./operations";

const storageKey = "subspace.onboarding";
const listeners = new Set<() => void>();
let memory: OnboardingMemory = "";

function subscribe(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

function read(): OnboardingMemory {
  try {
    const stored = localStorage.getItem(storageKey);
    return stored === "active" || stored === "dismissed" ? stored : "";
  } catch {
    return memory;
  }
}

function write(next: OnboardingMemory) {
  memory = next;
  try {
    localStorage.setItem(storageKey, next);
  } catch {
  }
  listeners.forEach((listener) => listener());
}

const steps: { id: OnboardingStep; title: string; summary: string; href: string; link: string }[] = [
  { id: "charter", title: "Set the charter", summary: "Rules every agent receives with its instructions.", href: "/instructions", link: "Instructions" },
  { id: "agents", title: "Register the leader and workers", summary: "Connect credentials are shown once.", href: "/agents", link: "Agents" },
  { id: "brief", title: "Create the first brief", summary: "Describe the goal, repositories, budget and allowed providers.", href: "/briefs", link: "Briefs" },
  { id: "plan", title: "Approve the leader's plan", summary: "The leader proposes tasks once it reads the brief.", href: "/plans", link: "Plans" },
];

function CharterStep({ charter, saved }: { charter: string; saved: () => void }) {
  const form = useSubmit();
  return <form className="stack" onSubmit={(event) => form.submit(event, async (fields) => {
    await api<Charter>("charter", "PUT", { text: value(fields, "text") });
    saved();
  })}>
    <label>Charter<textarea name="text" rows={6} defaultValue={charter} required /></label>
    <Feedback error={form.error} />
    <div><button className="primary" disabled={form.busy}>Save charter</button></div>
  </form>;
}

function AgentsStep({ leader, complete }: { leader: boolean; complete: (items: Registration[]) => void }) {
  const [bulk, setBulk] = useState(false);
  const form = useSubmit();
  return <form className="stack" onSubmit={(event) => form.submit(event, async (fields) => {
    const common = { role: value(fields, "role"), kind: value(fields, "kind"), model: value(fields, "model") };
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
    <label className="inline"><input type="checkbox" checked={bulk} onChange={(event) => setBulk(event.target.checked)} /> Bulk registration</label>
    <div className="form-grid">
      <label>{bulk ? "Agent ID prefix" : "Agent ID"}
        <input name="id" pattern={"[a-z0-9][a-z0-9_\\-]*"} maxLength={bulk ? 58 : 63} required />
      </label>
      {bulk && <label>Count<input name="count" type="number" min="1" max="1000" defaultValue="2" required /></label>}
      <label>Role<select name="role" key={String(leader)} defaultValue={leader ? "worker" : "leader"}>
        <option value="leader">Leader</option><option value="worker">Worker</option>
        <option value="tester">Tester</option><option value="reviewer">Reviewer</option>
      </select></label>
      <label>Kind<select name="kind" defaultValue="claude-code">
        <option value="claude-code">Claude Code</option><option value="codex">Codex</option>
        <option value="opencode">OpenCode</option><option value="other">Other</option>
      </select></label>
      <label>Model<input name="model" placeholder="claude-sonnet-5-5" required /></label>
    </div>
    <Feedback error={form.error} />
    <div><button className="primary" disabled={form.busy}>Register {bulk ? "agents" : "agent"}</button></div>
  </form>;
}

function Credentials({ items, dismiss }: { items: Registration[]; dismiss: () => void }) {
  return <section className="stack">
    <div className="heading"><h3>Agent credentials</h3><button type="button" onClick={dismiss}>Dismiss credentials</button></div>
    <p className="notice">Save these credentials now. Tokens are displayed once.</p>
    {items.map(({ agent, connect }, index) => (
      <details key={agent.id} open={index === 0}>
        <summary>{agent.id}</summary>
        <Secret label="Agent token" value={connect.token} />
        <Secret label="Join line" value={connect.join_line} />
        <Secret label="Claude Code" value={connect.snippets.claude_code} />
        <Secret label="Codex config.toml" value={connect.snippets.codex_toml} />
        <Secret label="OpenCode opencode.json" value={connect.snippets.opencode_json} />
      </details>
    ))}
  </section>;
}

function BriefStep({ created }: { created: () => void }) {
  const form = useSubmit();
  return <form className="stack" onSubmit={(event) => form.submit(event, async (fields) => {
    await api<Brief>("briefs", "POST", {
      title: value(fields, "title"), body: value(fields, "body"),
      repos: commaList(value(fields, "repos")),
      allowed_providers: commaList(value(fields, "providers")),
      max_cost_eur: Number(value(fields, "cost")),
      max_parallel_agents: Number(value(fields, "parallel")),
    });
    created();
  })}>
    <label>Title<input name="title" required /></label>
    <label>Brief<textarea name="body" rows={4} required /></label>
    <div className="form-grid">
      <label>Repositories<input name="repos" placeholder="owner/repo, owner/another" /></label>
      <label>Allowed providers<input name="providers" placeholder="All providers" /></label>
      <label>Budget (EUR)<input name="cost" type="number" min="0" step="0.01" defaultValue="50" required /></label>
      <label>Max parallel agents<input name="parallel" type="number" min="1" defaultValue="10" required /></label>
    </div>
    <Feedback error={form.error} />
    <div><button className="primary" disabled={form.busy}>Create brief</button></div>
  </form>;
}

function PlanStep({ plans, decided }: { plans: Plan[]; decided: () => void }) {
  const form = useSubmit();
  if (!plans.length) return <p className="meta" role="status">Waiting for the leader to propose a plan.</p>;
  return <div className="stack">
    {plans.map((plan) => <form key={plan.id} className="stack" onSubmit={(event) => form.submit(event, async () => {
      await api<Plan>(`plans/${encodeURIComponent(plan.id)}/approve`, "POST", { note: "" });
      decided();
    })}>
      <p>Plan {plan.id} for {plan.brief_id} by {plan.proposed_by} · {plan.tasks?.length ?? 0} tasks</p>
      <Feedback error={form.error} />
      <div className="actions"><button className="primary" disabled={form.busy}>Approve plan</button>
        <Link href="/plans">Review tasks</Link></div>
    </form>)}
  </div>;
}

export function Onboarding({ revisions }: { revisions: Revisions }) {
  const state = useResource<OnboardingState>("state", revisions.control);
  const charter = useResource<Charter>("charter", 0);
  const agents = useResource<{ agents: Agent[] }>("agents?limit=1000", revisions.agents);
  const briefs = useResource<{ briefs: Brief[] }>("briefs?limit=1000", revisions.briefs);
  const plans = useResource<{ plans: Plan[] }>("plans?status=proposed&limit=1000", revisions.plans);
  const memorized = useSyncExternalStore(subscribe, read, () => "");
  const [registered, setRegistered] = useState<Registration[] | null>(null);

  const loaded = state.data && charter.data && agents.data && briefs.data && plans.data;
  const firstRun = !!loaded && (state.data!.counts.agents === 0 || !briefs.data!.briefs?.length);
  useEffect(() => {
    if (firstRun && memorized === "") write("active");
  }, [firstRun, memorized]);

  if (!loaded) return null;
  const roster = (agents.data!.agents ?? []).filter((agent) => agent.status !== "revoked");
  const progress: OnboardingProgress = {
    charter: !!charter.data!.text?.trim(),
    agents: roster.some((agent) => agent.role === "leader") && roster.some((agent) => agent.role !== "leader"),
    brief: !!briefs.data!.briefs?.length,
    plan: !!briefs.data!.briefs?.some((brief) => brief.plan_status === "approved"),
  };
  const current = steps.find((step) => !progress[step.id])?.id;
  const visible = memorized !== "dismissed" && (firstRun || memorized === "active") && (!!current || !!registered);
  if (!visible) return null;

  const body: Record<OnboardingStep, ReactNode> = {
    charter: <CharterStep charter={charter.data!.text ?? ""} saved={charter.refresh} />,
    agents: <AgentsStep leader={roster.some((agent) => agent.role === "leader")}
      complete={(items) => { setRegistered(items); agents.refresh(); state.refresh(); }} />,
    brief: <BriefStep created={() => { briefs.refresh(); state.refresh(); }} />,
    plan: <PlanStep plans={plans.data!.plans ?? []} decided={() => { plans.refresh(); briefs.refresh(); }} />,
  };

  return <section className="panel stack" data-write aria-label="Getting started">
    <div className="heading">
      <h2>Getting started</h2>
      <button type="button" onClick={() => write("dismissed")}>Dismiss checklist</button>
    </div>
    <ol className="stack">
      {steps.map((step, index) => <li key={step.id} className="stack">
        <div className="heading">
          <div><strong>{index + 1}. {step.title}</strong> <span className="meta">{step.summary}</span></div>
          <div className="actions">
            <span className="pill" data-status={progress[step.id] ? "done" : step.id === current ? "in_progress" : "backlog"}>
              {progress[step.id] ? "Done" : step.id === current ? "Next" : "To do"}
            </span>
            <Link href={step.href}>{step.link}</Link>
          </div>
        </div>
        {step.id === current && body[step.id]}
      </li>)}
    </ol>
    {registered && <Credentials items={registered} dismiss={() => setRegistered(null)} />}
  </section>;
}
