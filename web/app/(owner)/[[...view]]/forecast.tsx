"use client";

import { money, optionalResource, useResource } from "../../../lib/client";
import { forecastMinutes } from "../../../lib/duration";
import { ResourceState } from "./states";

interface Range { p10: number; p50: number; p90: number }

export interface ForecastData {
  brief_id: string;
  generated_at: string;
  tasks: { total: number; complete: number; remaining: number };
  cost_eur: { used: number; budget: number; projected: Range; exceed_probability: number };
  schedule: { remaining_minutes: Range; completes_at: { p10: string; p50: string; p90: string } };
  burn: { eur_per_hour: number; tasks_per_hour: number; runway_hours: number | null; source: string };
  risks: { code: string; severity: string; message: string }[] | null;
}

const percent = new Intl.NumberFormat("en", { style: "percent", maximumFractionDigits: 0 });
const rate = new Intl.NumberFormat("en", { maximumFractionDigits: 2 });

function when(time: string) {
  return new Date(time).toLocaleString([], { dateStyle: "short", timeStyle: "short" });
}

function BudgetBar({ cost }: { cost: ForecastData["cost_eur"] }) {
  const { used, budget, projected } = cost;
  const top = Math.max(budget, projected.p90, used, 0.01) * 1.05;
  const x = (amount: number) => Math.min(100, Math.max(0, amount / top * 100));
  const over = budget > 0 && projected.p50 > budget;
  const summary = `Spent ${money(used)}, projected ${money(projected.p50)}${budget > 0 ? `, budget ${money(budget)}` : ", no budget set"}.`;
  return <svg viewBox="0 0 100 14" preserveAspectRatio="none" role="img" aria-label={summary}
    style={{ width: "100%", height: "1.5rem", borderRadius: "var(--radius)", background: "var(--panel-sunken)" }}>
    <rect x={x(projected.p10)} y="3" width={Math.max(0.5, x(projected.p90) - x(projected.p10))} height="8"
      style={{ fill: over ? "var(--danger)" : "var(--warn)", opacity: 0.3 }} />
    <rect x="0" y="3" width={x(used)} height="8" style={{ fill: "var(--action)" }} />
    <line x1={x(projected.p50)} x2={x(projected.p50)} y1="0" y2="14" strokeWidth="1.5" vectorEffect="non-scaling-stroke"
      style={{ stroke: over ? "var(--danger)" : "var(--text)" }}><title>{`Projected ${money(projected.p50)}`}</title></line>
    {budget > 0 && <line x1={x(budget)} x2={x(budget)} y1="0" y2="14" strokeWidth="1.5" strokeDasharray="3 2"
      vectorEffect="non-scaling-stroke" style={{ stroke: "var(--warn)" }}><title>{`Budget ${money(budget)}`}</title></line>}
  </svg>;
}

export function ForecastPanel({ briefID, revision }: { briefID: string; revision: number }) {
  const feed = useResource(`briefs/${encodeURIComponent(briefID)}/forecast`, revision, optionalResource<ForecastData>);
  const forecast = feed.data;
  if (forecast === null) return null;
  const risks = forecast?.risks ?? [];
  return <section className="panel stack">
    <div className="heading"><h2>Forecast</h2>
      {forecast && <span className="meta">{forecast.tasks.complete} / {forecast.tasks.total} tasks complete</span>}</div>
    <ResourceState error={feed.error} loading={!forecast && !feed.error} retry={feed.refresh} />
    {forecast && <>
      <div className="project-stats">
        <div className="panel"><span>Projected cost</span><strong>{money(forecast.cost_eur.projected.p50)}</strong>
          <span className="meta">{money(forecast.cost_eur.projected.p10)} – {money(forecast.cost_eur.projected.p90)}</span></div>
        <div className="panel"><span>Time remaining</span>
          <strong>{forecast.tasks.remaining ? forecastMinutes(forecast.schedule.remaining_minutes.p50) : "Done"}</strong>
          {forecast.tasks.remaining > 0 && <span className="meta">{forecastMinutes(forecast.schedule.remaining_minutes.p10)} – {forecastMinutes(forecast.schedule.remaining_minutes.p90)}</span>}</div>
        <div className="panel"><span>Burn rate</span><strong>{money(forecast.burn.eur_per_hour)}/h</strong>
          <span className="meta">{rate.format(forecast.burn.tasks_per_hour)} tasks/h
            {forecast.burn.runway_hours !== null && ` · ${forecastMinutes(forecast.burn.runway_hours * 60)} runway`}</span></div>
        <div className={`panel ${forecast.cost_eur.exceed_probability >= 0.5 ? "needs-attention" : ""}`}><span>Budget overrun risk</span>
          <strong>{forecast.cost_eur.budget > 0 ? percent.format(forecast.cost_eur.exceed_probability) : "No budget"}</strong></div>
      </div>
      <BudgetBar cost={forecast.cost_eur} />
      <p className="meta">Spent {money(forecast.cost_eur.used)} · projected {money(forecast.cost_eur.projected.p50)}
        {forecast.cost_eur.budget > 0 && ` · budget ${money(forecast.cost_eur.budget)}`}
        {forecast.tasks.remaining > 0 && ` · finishes ${when(forecast.schedule.completes_at.p50)}`}</p>
      {risks.length > 0 && <ul className="stack" aria-label="Forecast risks">
        {risks.map((risk) => <li key={risk.code}>
          <span className={`pill ${risk.severity === "high" ? "revoked" : "stale"}`}>{risk.severity}</span> {risk.message}</li>)}
      </ul>}
    </>}
  </section>;
}
