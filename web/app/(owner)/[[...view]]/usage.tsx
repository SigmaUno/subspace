"use client";

import Link from "next/link";
import { useContext, useState } from "react";
import { money, useResource } from "../../../lib/client";
import type {
  BriefBudget, ProviderStatus, UsageGroup, UsagePoint, UsageSeriesResponse, UsageSummary,
} from "../../../lib/usage-types";
import { Clock, Feedback } from "./components";

const groupings = [
  { id: "provider", name: "Provider" },
  { id: "model", name: "Model" },
  { id: "agent", name: "Agent" },
  { id: "brief", name: "Brief" },
] as const;

const dayMs = 86_400_000;
const days = 14;

const usd = new Intl.NumberFormat("en", { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 4 });
const count = new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 });

function span(now: number) {
  const to = (Math.floor(now / dayMs) + 1) * dayMs;
  return { from: new Date(to - days * dayMs).toISOString(), to: new Date(to).toISOString() };
}

const chart = { width: 720, height: 240, left: 64, right: 16, top: 16, bottom: 32 };
const innerWidth = chart.width - chart.left - chart.right;
const innerHeight = chart.height - chart.top - chart.bottom;
const dayLabel = (ts: string) => new Date(ts).toLocaleDateString([], { month: "short", day: "numeric", timeZone: "UTC" });

function DailyCost({ points }: { points: UsagePoint[] }) {
  const high = Math.max(...points.map((point) => point.cost_usd)) || 1;
  const slot = innerWidth / points.length;
  const y = (amount: number) => chart.top + (1 - amount / high) * innerHeight;
  return <svg className="hillclimb-chart" viewBox={`0 0 ${chart.width} ${chart.height}`} role="img" aria-label="Daily cost in US dollars">
    <line className="axis" x1={chart.left} x2={chart.left} y1={chart.top} y2={chart.top + innerHeight} />
    <line className="axis" x1={chart.left} x2={chart.left + innerWidth} y1={chart.top + innerHeight} y2={chart.top + innerHeight} />
    {[high, high / 2, 0].map((tick) => <text key={tick} className="tick" x={chart.left - 8} y={y(tick)} textAnchor="end" dominantBaseline="middle">{usd.format(tick)}</text>)}
    {points.map((point, index) => <rect key={point.ts} style={{ fill: "var(--action)" }}
      x={chart.left + index * slot + slot * 0.15} y={y(point.cost_usd)} width={slot * 0.7}
      height={chart.top + innerHeight - y(point.cost_usd)}>
      <title>{dayLabel(point.ts)}: {usd.format(point.cost_usd)} · {point.requests} requests</title>
    </rect>)}
    <text className="tick" x={chart.left + slot / 2} y={chart.height - 8} textAnchor="middle">{dayLabel(points[0].ts)}</text>
    {points.length > 1 && <text className="tick" x={chart.left + innerWidth - slot / 2} y={chart.height - 8} textAnchor="middle">{dayLabel(points[points.length - 1].ts)}</text>}
  </svg>;
}

function CostTable({ groups, name }: { groups: UsageGroup[]; name: string }) {
  if (!groups.length) return <p className="empty">No usage recorded in this period.</p>;
  return <div className="table-wrap">
    <table>
      <thead><tr><th>{name}</th><th>Cost</th><th>Requests</th><th>Tokens in</th><th>Tokens out</th><th>Cached</th><th>Rate limited</th></tr></thead>
      <tbody>{groups.map((group) => (
        <tr key={group.key}>
          <th scope="row">{group.key || "—"}</th>
          <td>{usd.format(group.cost_usd)}</td>
          <td>{count.format(group.requests)}</td>
          <td>{count.format(group.tokens_in)}</td>
          <td>{count.format(group.tokens_out)}</td>
          <td>{count.format(group.cache_read)}</td>
          <td className={group.rate_limited ? "stale" : undefined}>{group.rate_limited}</td>
        </tr>
      ))}</tbody>
    </table>
  </div>;
}

function BudgetBurn({ briefs }: { briefs: BriefBudget[] }) {
  if (!briefs.length) return <p className="empty">No briefs.</p>;
  return <div className="table-wrap">
    <table>
      <thead><tr><th>Brief</th><th>Status</th><th>Budget</th><th>Spent</th><th>Burn</th></tr></thead>
      <tbody>{briefs.map((brief) => {
        const over = brief.budget_usd > 0 && brief.spent_usd > brief.budget_usd;
        return <tr key={brief.brief_id}>
          <th scope="row">
            <Link href={`/briefs/${encodeURIComponent(brief.brief_id)}`}>{brief.brief_id}</Link>
            <div className="meta">{brief.title}</div>
          </th>
          <td><span className="pill">{brief.status}</span></td>
          <td>{brief.budget_eur > 0 ? <>{money(brief.budget_eur)}<div className="meta">{usd.format(brief.budget_usd)}</div></> : "No budget"}</td>
          <td className={over ? "stale" : undefined}>{usd.format(brief.spent_usd)}</td>
          <td>{brief.budget_usd > 0 ? <div className="progress">
            <progress max={brief.budget_usd} value={Math.min(brief.spent_usd, brief.budget_usd)} aria-label={`${brief.brief_id} budget burn`} />
            <span className={over ? "stale" : "meta"}>{Math.round((brief.spent_usd / brief.budget_usd) * 100)}%</span>
          </div> : "—"}</td>
        </tr>;
      })}</tbody>
    </table>
  </div>;
}

const statusClass = { ok: "active", throttled: "revoked", saturated: "paused" } as const;

function Meter({ name, used, limit, unit }: { name: string; used: number; limit: number; unit: string }) {
  return <div className="stack">
    <div className="heading"><span className="meta">{name}</span>
      <span className={limit > 0 && used >= limit ? "stale" : "meta"}>{used}{limit > 0 ? ` / ${limit}` : ""} {unit}{limit > 0 ? "" : " · no limit"}</span></div>
    {limit > 0 && <progress max={limit} value={Math.min(used, limit)} aria-label={`${name} in use`} />}
  </div>;
}

function ProviderTile({ provider }: { provider: ProviderStatus }) {
  return <article className="panel stack">
    <div className="heading"><h3>{provider.provider}</h3>
      <span className={`pill ${statusClass[provider.status] ?? ""}`}>{provider.status}</span></div>
    <Meter name="Concurrency" used={provider.in_use} limit={provider.max_concurrency} unit="agents" />
    <Meter name="Requests last minute" used={provider.requests_1m} limit={provider.requests_per_min} unit="requests" />
    <p className={provider.rate_limited_5m ? "stale" : "meta"}>
      {provider.rate_limited_5m} rate limited in 5 min · {provider.rate_limited_1h} in 1 h
    </p>
    <p className="meta">
      {provider.last_rate_limited_at
        ? <>Last rate limit <time dateTime={provider.last_rate_limited_at}>{new Date(provider.last_rate_limited_at).toLocaleString([], { dateStyle: "short", timeStyle: "medium" })}</time></>
        : "No rate limits in the last hour"}
    </p>
  </article>;
}

export function UsageView({ revision }: { revision: number }) {
  const now = useContext(Clock);
  const [by, setBy] = useState<(typeof groupings)[number]["id"]>("provider");
  const range = now ? span(now) : null;
  const summary = useResource<UsageSummary>(range && `usage/summary?${new URLSearchParams({ ...range, group_by: by })}`, revision);
  const series = useResource<UsageSeriesResponse>(range && `usage/series?${new URLSearchParams({ ...range, bucket: "day" })}`, revision);
  const providers = useResource<{ providers: ProviderStatus[] }>("usage/providers", revision);
  const current = summary.data?.group_by === by ? summary.data : undefined;
  const points = series.data?.series?.[0]?.points;
  const credits = summary.data?.credits;

  return <>
    <div className="heading"><h1>Usage</h1>
      {summary.data && <div className="actions">
        <span className="pill">{usd.format(summary.data.totals.cost_usd)} last {days} days</span>
        {credits?.remaining_usd != null && <span className={`pill ${credits.remaining_usd <= 0 ? "revoked" : ""}`}>{usd.format(credits.remaining_usd)} credits left</span>}
      </div>}
    </div>
    <Feedback error={summary.error || series.error || providers.error} loading={!summary.data || !providers.data} />

    <section className="stack">
      <h2>Provider availability</h2>
      {providers.data && !providers.data.providers?.length && <p className="empty">No providers seen yet.</p>}
      {!!providers.data?.providers?.length && <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(16rem, 1fr))", gap: ".8rem" }}>
        {providers.data.providers.map((provider) => <ProviderTile key={provider.provider} provider={provider} />)}
      </div>}
    </section>

    <section className="panel stack">
      <h2>Daily cost</h2>
      {points?.length ? <DailyCost points={points} /> : series.data && <p className="empty">No usage recorded in this period.</p>}
    </section>

    <section className="stack">
      <div className="heading"><h2>Cost by {by}</h2>
        <div className="actions" role="group" aria-label="Group cost by">
          {groupings.map((group) => <button key={group.id} aria-pressed={group.id === by} className={group.id === by ? "primary" : undefined} onClick={() => setBy(group.id)}>{group.name}</button>)}
        </div>
      </div>
      {current ? <CostTable groups={current.groups ?? []} name={groupings.find((group) => group.id === by)!.name} /> : !summary.error && <p className="muted" role="status">Loading…</p>}
    </section>

    <section className="stack">
      <h2>Brief budgets</h2>
      {summary.data && <BudgetBurn briefs={summary.data.briefs ?? []} />}
    </section>
  </>;
}
