"use client";

import "./bulk.css";
import { useState } from "react";
import { api, useSubmit, value } from "../../../lib/client";
import type { AgentAction, BulkResponse, TaskAction } from "../../../lib/bulk-types";
import type { Agent } from "../../../lib/types";

const maxIDs = 500;

interface Field {
  name: string;
  label: string;
  type: "agent" | "text" | "number" | "status";
}

interface Spec {
  action: TaskAction | AgentAction;
  name: string;
  destructive?: boolean;
  fields: Field[];
}

const reason: Field = { name: "reason", label: "Reason", type: "text" };
const note: Field = { name: "note", label: "Note", type: "text" };
const status: Field = { name: "status", label: "Reopen as", type: "status" };
const agent: Field = { name: "agent_id", label: "Agent", type: "agent" };

const specs: Record<TaskAction | AgentAction, Spec> = {
  assign: { action: "assign", name: "Assign", fields: [agent] },
  reassign: { action: "reassign", name: "Reassign", fields: [agent, reason] },
  cancel: { action: "cancel", name: "Cancel", destructive: true, fields: [reason] },
  reopen: { action: "reopen", name: "Reopen", fields: [status, note] },
  unblock: { action: "unblock", name: "Unblock", fields: [status, note] },
  set_budget: { action: "set_budget", name: "Set budget", fields: [
    { name: "max_minutes", label: "Max minutes", type: "number" },
    { name: "max_cost_eur", label: "Max cost (EUR)", type: "number" },
  ] },
  pause: { action: "pause", name: "Pause", destructive: true, fields: [reason] },
  resume: { action: "resume", name: "Resume", fields: [] },
  revoke: { action: "revoke", name: "Revoke", destructive: true, fields: [reason] },
  "rotate-token": { action: "rotate-token", name: "Rotate token", destructive: true, fields: [] },
};

export const taskActions: TaskAction[] = ["assign", "reassign", "unblock", "reopen", "set_budget", "cancel"];
export const quickTaskActions: TaskAction[] = ["assign", "reassign", "cancel", "reopen", "set_budget"];
export const agentActions: AgentAction[] = ["pause", "resume", "rotate-token", "revoke"];

export function useBulk() {
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [result, setResult] = useState<BulkResponse | null>(null);
  return {
    selected, result,
    toggle: (id: string) => setSelected((current) => {
      const next = new Set(current);
      if (!next.delete(id)) next.add(id);
      return next;
    }),
    replace: (ids: Iterable<string>) => setSelected(new Set(ids)),
    clear: () => setSelected(new Set()),
    report: (response: BulkResponse) => {
      setSelected(new Set(response.results.filter((entry) => !entry.ok).map((entry) => entry.id)));
      setResult(response);
    },
    dismiss: () => setResult(null),
  };
}

export type Bulk = ReturnType<typeof useBulk>;

export function SelectBox({ id, bulk, name }: { id: string; bulk: Bulk; name: string }) {
  return <label className="select-box">
    <input type="checkbox" aria-label={`Select ${name}`} checked={bulk.selected.has(id)} onChange={() => bulk.toggle(id)} />
  </label>;
}

export function SelectAll({ ids, bulk, name }: { ids: string[]; bulk: Bulk; name: string }) {
  const all = ids.length > 0 && ids.every((id) => bulk.selected.has(id));
  return <label className="select-box">
    <input type="checkbox" aria-label={`Select all ${name}`} checked={all} disabled={!ids.length}
      onChange={() => bulk.replace(all ? [] : ids)} />
    Select all
  </label>;
}

export function BulkActions({ kind, ids, agents = [], actions, onResult }: {
  kind: "tasks" | "agents"; ids: string[]; agents?: Agent[]; actions: (TaskAction | AgentAction)[];
  onResult: (response: BulkResponse) => void;
}) {
  const [active, setActive] = useState<Spec | null>(null);
  const [confirming, setConfirming] = useState(false);
  const form = useSubmit();
  const noun = `${ids.length} ${kind === "tasks" ? "task" : "agent"}${ids.length === 1 ? "" : "s"}`;

  function close() {
    setActive(null);
    setConfirming(false);
  }

  if (ids.length > maxIDs) return <span className="error" role="alert">Select at most {maxIDs} at a time.</span>;
  if (!active) {
    return <div className="actions">
      {actions.map((action) => <button key={action} type="button" className={specs[action].destructive ? "danger" : undefined}
        onClick={() => { setActive(specs[action]); }}>{specs[action].name}</button>)}
    </div>;
  }

  return <form className="bulk-form" onSubmit={(event) => form.submit(event, async (fields) => {
    if (active.destructive && !confirming) {
      setConfirming(true);
      return;
    }
    const params: Record<string, string | number> = {};
    for (const field of active.fields) {
      const text = value(fields, field.name);
      if (text) params[field.name] = field.type === "number" ? Number(text) : text;
    }
    const path = kind === "tasks" ? "tasks/bulk" : "agents/bulk-action";
    const response = await api<BulkResponse>(path, "POST", { ids, action: active.action, params });
    close();
    onResult(response);
  })}>
    {active.fields.map((field) => <label key={field.name}>{field.label}
      {field.type === "agent" && <select name={field.name} required>
        <option value="">Choose agent</option>
        {agents.filter((entry) => entry.status === "active").map((entry) =>
          <option key={entry.id} value={entry.id}>{entry.id} ({entry.role})</option>)}
      </select>}
      {field.type === "status" && <select name={field.name} defaultValue="ready">
        <option value="ready">ready</option><option value="assigned">assigned</option>
      </select>}
      {field.type === "text" && <input name={field.name} />}
      {field.type === "number" && <input name={field.name} type="number" min="0" step="any" />}
    </label>)}
    {confirming && <span className="bulk-confirm" role="alert">{active.name} {noun}? This cannot be undone.</span>}
    <button type="submit" className={active.destructive ? "danger" : "primary"} disabled={form.busy}>
      {confirming ? `Confirm ${active.name.toLowerCase()}` : `${active.name} ${noun}`}
    </button>
    <button type="button" onClick={close} disabled={form.busy}>Back</button>
    {form.error && <span className="error" role="alert">{form.error}</span>}
  </form>;
}

export function BulkBar({ kind, bulk, agents, actions }: {
  kind: "tasks" | "agents"; bulk: Bulk; agents?: Agent[]; actions: (TaskAction | AgentAction)[];
}) {
  if (!bulk.selected.size) return null;
  return <div className="bulk-bar" data-write role="region" aria-label="Bulk actions">
    <strong>{bulk.selected.size} selected</strong>
    <button type="button" onClick={bulk.clear}>Clear</button>
    <BulkActions kind={kind} ids={[...bulk.selected]} agents={agents}
      actions={actions} onResult={bulk.report} />
  </div>;
}

export function QuickMenu({ taskID, agents, bulk }: { taskID: string; agents: Agent[]; bulk: Bulk }) {
  return <details className="quick-menu" data-write>
    <summary>Quick actions</summary>
    <BulkActions kind="tasks" ids={[taskID]} agents={agents} actions={quickTaskActions} onResult={bulk.report} />
  </details>;
}

export function BulkToast({ bulk }: { bulk: Bulk }) {
  const result = bulk.result;
  if (!result) return null;
  const failures = result.results.filter((entry) => !entry.ok);
  const tokens = result.results.filter((entry) => entry.connect?.token);
  return <aside className="bulk-toast panel stack" role={failures.length ? "alert" : "status"}>
    <div className="heading">
      <strong>{specs[result.action as keyof typeof specs]?.name ?? result.action}: {result.succeeded} succeeded{failures.length > 0 && `, ${failures.length} failed`}</strong>
      <button type="button" onClick={bulk.dismiss}>Dismiss</button>
    </div>
    {failures.length > 0 && <ul aria-label="Failures">
      {failures.map((entry) => <li key={entry.id}><code>{entry.id}</code>: {entry.error?.message ?? "Failed."}</li>)}
    </ul>}
    {tokens.length > 0 && <>
      <p className="meta">New tokens are shown once. Copy them now.</p>
      <ul aria-label="New tokens">
        {tokens.map((entry) => <li key={entry.id}><code>{entry.id}</code>: <code>{entry.connect?.token}</code></li>)}
      </ul>
    </>}
  </aside>;
}
