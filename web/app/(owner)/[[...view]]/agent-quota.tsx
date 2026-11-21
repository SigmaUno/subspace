"use client";

import { api, money, useResource, useSubmit, value } from "../../../lib/client";
import type { AgentQuota, AgentQuotaUsage, AgentWithQuota } from "../../../lib/quota-types";
import { Feedback } from "./components";
import { utc as when } from "./format";
import { ResourceState } from "./states";
import { useToast } from "./ui";

const levelClass = { off: "", ok: "active", warning: "paused", exceeded: "revoked" } as const;


function amount(form: FormData, key: string, integer: boolean) {
  const parsed = Number(value(form, key) || "0");
  if (!Number.isFinite(parsed) || parsed < 0 || (integer && !Number.isInteger(parsed))) {
    throw new Error(integer ? "Counts must be whole numbers, zero or more." : "The cost cap must be zero or more.");
  }
  return parsed;
}

const used = (consumed: string, limit: number, format: (n: number) => string = String) => limit > 0 ? `${consumed} / ${format(limit)}` : `${consumed} · no limit`;

function Consumption({ quota, usage }: { quota: AgentQuota; usage: AgentQuotaUsage }) {
  return <dl className="stack">
    <div><dt className="meta">Cost today</dt><dd>{used(money(usage.cost_eur), quota.max_cost_eur_per_day, money)}</dd></div>
    <div><dt className="meta">Tasks started today</dt><dd>{used(String(usage.tasks), quota.max_tasks_per_day)}</dd></div>
    <div><dt className="meta">Running now</dt><dd>{used(String(usage.concurrent_tasks), quota.max_concurrent_tasks)}</dd></div>
  </dl>;
}

export function AgentQuotaPanel({ id, revision, revoked }: { id: string; revision: number; revoked: boolean }) {
  const path = `agents/${encodeURIComponent(id)}`;
  const agent = useResource<AgentWithQuota>(path, revision);
  const form = useSubmit();
  const toast = useToast();
  const current = agent.data;

  return <section className="panel stack">
    <div className="heading"><h2>Daily quota</h2>
      {current && <span className={`pill ${levelClass[current.quota_usage.level]}`}>{current.quota_usage.level}</span>}</div>
    <ResourceState error={agent.error} loading={!current && !agent.error} retry={agent.refresh} rows={2} />
    {current && <>
      <Consumption quota={current.quota} usage={current.quota_usage} />
      <p className="meta">The window is the UTC day and resets {when(current.quota_usage.resets_at)}. Claims and assignments over a limit are refused; 0 means unlimited. A decision and notification are raised at 90%.</p>
      <form key={JSON.stringify(current.quota)} className="actions" onSubmit={(event) => form.submit(event, async (fields) => {
        const quota: AgentQuota = {
          max_cost_eur_per_day: amount(fields, "max_cost_eur_per_day", false),
          max_tasks_per_day: amount(fields, "max_tasks_per_day", true),
          max_concurrent_tasks: amount(fields, "max_concurrent_tasks", true),
        };
        await api(`${path}/quota`, "PUT", quota);
        agent.refresh();
        toast(`Saved the quota of ${id}.`);
      })}>
        <fieldset className="actions" disabled={form.busy || revoked}>
          <label>Cost per day (EUR)
            <input name="max_cost_eur_per_day" type="number" min="0" step="0.01" defaultValue={current.quota.max_cost_eur_per_day} />
          </label>
          <label>Tasks per day
            <input name="max_tasks_per_day" type="number" min="0" step="1" defaultValue={current.quota.max_tasks_per_day} />
          </label>
          <label>Concurrent tasks
            <input name="max_concurrent_tasks" type="number" min="0" step="1" defaultValue={current.quota.max_concurrent_tasks} />
          </label>
          <button className="primary">Save quota</button>
        </fieldset>
      </form>
      <Feedback error={form.error} />
    </>}
  </section>;
}
