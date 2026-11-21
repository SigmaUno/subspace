"use client";

import type { Alert, AlertList } from "../../../lib/settings-types";
import { formatTime } from "./format";
import { EmptyState } from "./states";

const severityClass = { warning: "paused", critical: "revoked" } as const;

function AlertRows({ alerts }: { alerts: Alert[] }) {
  return <div className="table-wrap">
    <table>
      <thead><tr><th>Alert</th><th>Severity</th><th>Summary</th><th>Raised</th><th>Resolved</th></tr></thead>
      <tbody>{alerts.map((alert) => <tr key={`${alert.id}:${alert.raised_at}`}>
        <th scope="row">{alert.title} <span className="meta">{alert.id}</span></th>
        <td><span className={`pill ${severityClass[alert.severity]}`}>{alert.severity}</span></td>
        <td>
          {alert.summary}
          {[alert.brief_id, alert.task_id, alert.agent_id].some(Boolean) &&
            <span className="meta"> · {[alert.brief_id, alert.task_id, alert.agent_id].filter(Boolean).join(" · ")}</span>}
        </td>
        <td>{formatTime(alert.raised_at)}</td>
        <td>{formatTime(alert.resolved_at)}</td>
      </tr>)}</tbody>
    </table>
  </div>;
}

export function AlertsPanel({ list }: { list: AlertList }) {
  const alerts = list.alerts ?? [];
  const active = alerts.filter((alert) => alert.status === "active");
  const resolved = alerts.filter((alert) => alert.status === "resolved");
  return <>
    <section className="panel stack">
      <div className="heading"><h2>Active alerts</h2>
        <span className={`pill ${active.length ? "revoked" : "active"}`}>{active.length ? `${active.length} active` : "all clear"}</span></div>
      {!active.length && <EmptyState>No active alerts.</EmptyState>}
      {!!active.length && <AlertRows alerts={active} />}
    </section>
    <section className="panel stack">
      <div className="heading"><h2>Recently resolved</h2></div>
      {!resolved.length && <EmptyState>No resolved alerts yet.</EmptyState>}
      {!!resolved.length && <AlertRows alerts={resolved} />}
      <p className="meta">Alerts are held in memory and cleared on restart. Thresholds are set with SUBSPACE_ALERTS_* environment variables.</p>
    </section>
  </>;
}
