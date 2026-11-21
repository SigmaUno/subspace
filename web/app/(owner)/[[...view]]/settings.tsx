"use client";

import { useState } from "react";
import { api, optionalResource, useResource, useSubmit } from "../../../lib/client";
import type {
  About, AlertList, Backup, BackupList, BreakerList, NotifyStatus, RetentionStatus, SinkResult, TokenList, WebhookList,
} from "../../../lib/settings-types";
import type { Control } from "../../../lib/types";
import { AlertsPanel } from "./admin-alerts";
import { BreakersPanel } from "./admin-breakers";
import { RetentionPanel } from "./admin-retention";
import { formatSize, formatTime } from "./admin-shared";
import { TokensPanel } from "./admin-tokens";
import { WebhooksPanel } from "./admin-webhooks";
import { AnnouncementsView } from "./announcements";
import { CharterView } from "./charter";
import { Feedback } from "./components";
import { InstructionsView } from "./instructions";
import type { Revisions } from "./live";
import { CapacityView } from "./operations";
import { OrgBudget } from "./org-budget";
import { SchedulesView } from "./schedules";
import { EmptyState, ResourceState } from "./states";

function formatDuration(seconds: number) {
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor(seconds % 86400 / 3600);
  const minutes = Math.floor(seconds % 3600 / 60);
  if (days) return `${days}d ${hours}h ${minutes}m`;
  if (hours) return `${hours}h ${minutes}m`;
  return `${minutes}m ${Math.floor(seconds % 60)}s`;
}

function NotificationsPanel({ status }: { status: NotifyStatus }) {
  const [results, setResults] = useState<SinkResult[]>();
  const form = useSubmit();
  return <section className="panel stack">
    <div className="heading"><h2>Notifications</h2>
      <span className={`pill ${status.enabled ? "active" : ""}`}>{status.enabled ? "enabled" : "no sinks configured"}</span></div>
    {!status.sinks.length && <EmptyState>No notification sinks are configured.</EmptyState>}
    {!!status.sinks.length && <ul className="stack">
      {status.sinks.map((sink) => <li key={sink}><span className="pill">{sink}</span></li>)}
    </ul>}
    <p className="meta">{status.sent} sent · {status.failed} failed · {status.dropped} dropped · {status.queued} queued</p>
    <form className="stack" onSubmit={(event) => form.submit(event, async () => {
      const response = await api<{ results: SinkResult[] }>("notify/test", "POST", {});
      setResults(response.results);
    })}>
      <div className="actions">
        <button className="primary" disabled={form.busy || !status.sinks.length}>Send test notification</button>
      </div>
      <Feedback error={form.error} />
    </form>
    {results && <ul className="stack" role="status" aria-label="Test results">
      {results.map((result) => <li key={result.sink}>
        <span className={`pill ${result.ok ? "active" : "revoked"}`}>{result.ok ? "delivered" : "failed"}</span>{" "}
        {result.sink}{result.error && <span className="meta"> · {result.error}</span>}
      </li>)}
    </ul>}
  </section>;
}

function BackupsPanel({ list, refresh }: { list: BackupList; refresh: () => void }) {
  const form = useSubmit();
  const backups: Backup[] = list.backups ?? [];
  return <section className="panel stack">
    <div className="heading"><h2>Backups</h2></div>
    <form className="stack" onSubmit={(event) => form.submit(event, async () => {
      await api<{ path: string; bytes: number }>("backup", "POST", {});
      refresh();
    })}>
      <div className="actions"><button className="primary" disabled={form.busy}>Create backup now</button></div>
      <Feedback error={form.error} />
    </form>
    {!backups.length && <EmptyState>No backups yet.</EmptyState>}
    {!!backups.length && <div className="table-wrap">
      <table>
        <thead><tr><th>Name</th><th>Created</th><th>Size</th><th>Integrity</th></tr></thead>
        <tbody>{backups.map((backup) => (
          <tr key={backup.name}>
            <th scope="row">{backup.name}</th>
            <td>{formatTime(backup.created_at)}</td>
            <td>{formatSize(backup.bytes)}</td>
            <td><span className={`pill ${backup.verified ? "active" : "revoked"}`}>{backup.verified ? "verified" : "failed"}</span></td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
  </section>;
}

function AboutPanel({ version, about }: { version?: string; about?: About | null }) {
  const rows: [string, string][] = [
    ["Version", about?.version ?? version ?? "—"],
    ["Uptime", about?.uptime_seconds === undefined ? "—" : formatDuration(about.uptime_seconds)],
    ["Database size", about?.db_bytes === undefined ? "—" : formatSize(about.db_bytes)],
    ["Backend", about?.backend ?? "—"],
  ];
  return <section className="panel stack">
    <div className="heading"><h2>About</h2></div>
    <dl className="stack">
      {rows.map(([name, text]) => <div key={name} className="heading"><dt className="meta">{name}</dt><dd>{text}</dd></div>)}
    </dl>
  </section>;
}

export function SettingsView({ revisions }: { revisions: Revisions }) {
  const state = useResource<Control>("state", revisions.control);
  const notify = useResource("notify/status", revisions.control, optionalResource<NotifyStatus>);
  const backups = useResource("backups", 0, optionalResource<BackupList>);
  const tokens = useResource("tokens", 0, optionalResource<TokenList>);
  const alerts = useResource("alerts", revisions.control, optionalResource<AlertList>);
  const retention = useResource("retention", 0, optionalResource<RetentionStatus>);
  const webhooks = useResource("webhooks-out", 0, optionalResource<WebhookList>);
  const breakers = useResource("breakers", revisions.control, optionalResource<BreakerList>);
  const about = useResource("about", 0, optionalResource<About>);
  const [selected, setSelected] = useState("charter");

  const tabs = [
    { id: "charter", name: "Charter", show: true },
    { id: "capacity", name: "Capacity and limits", show: true },
    { id: "instructions", name: "Instructions", show: true },
    { id: "announcements", name: "Announcements", show: true },
    { id: "schedules", name: "Schedules", show: true },
    { id: "notifications", name: "Notifications", show: notify.data !== null },
    { id: "backups", name: "Backups", show: backups.data !== null },
    { id: "tokens", name: "Tokens", show: tokens.data !== null },
    { id: "alerts", name: "Alerts", show: alerts.data !== null },
    { id: "retention", name: "Retention", show: retention.data !== null },
    { id: "webhooks", name: "Webhooks", show: webhooks.data !== null },
    { id: "breakers", name: "Provider breakers", show: breakers.data !== null },
    { id: "about", name: "About", show: true },
  ].filter((tab) => tab.show);
  const active = tabs.some((tab) => tab.id === selected) ? selected : "charter";

  return <>
    <div className="actions" role="group" aria-label="Settings">
      {tabs.map((tab) => <button key={tab.id} className="text-button" aria-pressed={tab.id === active}
        onClick={() => setSelected(tab.id)}>{tab.name}</button>)}
    </div>
    {active === "charter" && <CharterView />}
    {active === "capacity" && <>
      <CapacityView revision={revisions.capacity} />
      <OrgBudget revision={revisions.capacity} />
    </>}
    {active === "instructions" && <InstructionsView />}
    {active === "announcements" && <AnnouncementsView />}
    {active === "schedules" && <SchedulesView />}
    {active === "notifications" && <>
      <ResourceState error={notify.error} loading={notify.data === undefined} retry={notify.refresh} rows={2} />
      {notify.data && <NotificationsPanel status={notify.data} />}
    </>}
    {active === "backups" && <>
      <ResourceState error={backups.error} loading={backups.data === undefined} retry={backups.refresh} rows={3} />
      {backups.data && <BackupsPanel list={backups.data} refresh={backups.refresh} />}
    </>}
    {active === "tokens" && <>
      <ResourceState error={tokens.error} loading={tokens.data === undefined} retry={tokens.refresh} rows={3} />
      {tokens.data && <TokensPanel list={tokens.data} refresh={tokens.refresh} />}
    </>}
    {active === "alerts" && <>
      <ResourceState error={alerts.error} loading={alerts.data === undefined} retry={alerts.refresh} rows={3} />
      {alerts.data && <AlertsPanel list={alerts.data} />}
    </>}
    {active === "retention" && <>
      <ResourceState error={retention.error} loading={retention.data === undefined} retry={retention.refresh} rows={3} />
      {retention.data && <RetentionPanel status={retention.data} refresh={retention.refresh} />}
    </>}
    {active === "webhooks" && <>
      <ResourceState error={webhooks.error} loading={webhooks.data === undefined} retry={webhooks.refresh} rows={3} />
      {webhooks.data && <WebhooksPanel list={webhooks.data} refresh={webhooks.refresh} />}
    </>}
    {active === "breakers" && <>
      <ResourceState error={breakers.error} loading={breakers.data === undefined} retry={breakers.refresh} rows={3} />
      {breakers.data && <BreakersPanel list={breakers.data} refresh={breakers.refresh} />}
    </>}
    {active === "about" && <>
      <ResourceState error={state.error} loading={state.data === undefined} retry={state.refresh} rows={2} />
      <AboutPanel version={state.data?.version} about={about.data} />
    </>}
  </>;
}
