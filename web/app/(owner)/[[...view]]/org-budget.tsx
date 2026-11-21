"use client";

import { useState } from "react";
import { api, money, useResource, useSubmit, value } from "../../../lib/client";
import type { SpendLimits, SpendWindow } from "../../../lib/usage-types";
import { Feedback } from "./components";
import { ResourceState } from "./states";

const levelClass = { off: "", ok: "active", warning: "paused", exceeded: "revoked" } as const;

const when = (iso: string) => `${new Date(iso).toLocaleString([], { dateStyle: "short", timeStyle: "short", timeZone: "UTC" })} UTC`;

function outlook(spend: SpendWindow) {
  if (spend.level === "off") return "No cap set.";
  if (spend.level === "exceeded") return `Cap reached. Claims and spawns stay paused until ${when(spend.resets_at)} or the cap is raised.`;
  if (spend.projected_exhaustion_at) return `Projected to run out ${when(spend.projected_exhaustion_at)}.`;
  return `Not projected to run out before the reset at ${when(spend.resets_at)}.`;
}

function WindowTile({ spend }: { spend: SpendWindow }) {
  return <article className="stack">
    <div className="heading"><h3>{spend.window === "daily" ? "Today" : "This week"}</h3>
      <span className={`pill ${levelClass[spend.level]}`}>{spend.level}</span></div>
    <div className="heading"><span className="meta">Spent</span>
      <span className={spend.level === "exceeded" ? "stale" : "meta"}>
        {money(spend.spent_eur)}{spend.cap_eur > 0 ? ` / ${money(spend.cap_eur)} · ${Math.round(spend.percent)}%` : ""}
      </span></div>
    {spend.cap_eur > 0 && <progress max={spend.cap_eur} value={Math.min(spend.spent_eur, spend.cap_eur)} aria-label={`${spend.window} spend`} />}
    <p className="meta">{outlook(spend)}</p>
  </article>;
}

function cap(form: FormData, key: string) {
  const amount = Number(value(form, key) || "0");
  if (!Number.isFinite(amount) || amount < 0) throw new Error("Caps must be zero or more.");
  return amount;
}

export function OrgBudget({ revision }: { revision: number }) {
  const limits = useResource<SpendLimits>("limits", revision);
  const [saved, setSaved] = useState(false);
  const form = useSubmit();
  const current = limits.data;

  return <section className="panel stack">
    <div className="heading"><h2>Organization spend caps</h2>
      {current?.org_budget_paused && <span className="pill revoked">paused by org budget</span>}</div>
    <ResourceState error={limits.error} loading={!current} retry={limits.refresh} rows={2} />
    {current && <>
      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(16rem, 1fr))", gap: ".8rem" }}>
        {current.windows.map((spend) => <WindowTile key={spend.window} spend={spend} />)}
      </div>
      <form key={`${current.daily_cost_eur}:${current.weekly_cost_eur}`} className="actions" data-write onSubmit={(event) => form.submit(event, async (fields) => {
        const response = await api<SpendLimits>("limits", "PUT", { daily_cost_eur: cap(fields, "daily_cost_eur"), weekly_cost_eur: cap(fields, "weekly_cost_eur") });
        limits.setData(response);
        limits.refresh();
        setSaved(true);
      })}>
        <fieldset className="actions" disabled={form.busy}>
          <label>Daily cap (EUR, 0 = off)
            <input name="daily_cost_eur" type="number" min="0" step="0.01" defaultValue={current.daily_cost_eur} onChange={() => setSaved(false)} />
          </label>
          <label>Weekly cap (EUR, 0 = off)
            <input name="weekly_cost_eur" type="number" min="0" step="0.01" defaultValue={current.weekly_cost_eur} onChange={() => setSaved(false)} />
          </label>
          <button className="primary">Save caps</button>
        </fieldset>
      </form>
      <Feedback error={form.error} />
      {saved && <span className="meta" role="status">Caps saved.</span>}
      <p className="meta">Windows are UTC. At 80% a decision and notification are raised; at 100% everything pauses with reason org_budget.</p>
    </>}
  </section>;
}
