"use client";

import { useState, type ReactNode } from "react";
import { api, optionalResource, useResource, useSubmit } from "../../../lib/client";
import type {
  About, AlertList, Backup, BackupList, BreakerList, NotifyStatus, RetentionStatus, SinkResult, TokenList, WebhookList,
} from "../../../lib/settings-types";
import type { DigestPreview } from "../../../lib/digest-types";
import type { Control } from "../../../lib/types";
import { uptime } from "../../../lib/duration";
import { AlertsPanel } from "./admin-alerts";
import { BreakersPanel } from "./admin-breakers";
import { RetentionPanel } from "./admin-retention";
import { formatSize, formatTime } from "./format";
import { TokensPanel } from "./admin-tokens";
import { WebhooksPanel } from "./admin-webhooks";
import { AnnouncementsView } from "./announcements";
import { CharterView } from "./charter";
import { Feedback } from "./components";
import { DigestPanel } from "./digest";
import { InstructionsView } from "./instructions";
import type { Revisions } from "./live";
import { PlaybooksPanel } from "./playbooks";
import { CapacityView } from "./operations";
import { OrgBudget } from "./org-budget";
import { PushPanel } from "./push";
import { RepositoriesView } from "./repos";
import { SchedulesView } from "./schedules";
import { EmptyState, PageHeading, ResourceState } from "./states";
import { useToast } from "./ui";

function NotificationsPanel({ status }: { status: NotifyStatus }) {
  const [results, setResults] = useState<SinkResult[]>();
  const form = useSubmit();
  const toast = useToast();
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
      toast(`Test notification sent to ${response.results.length} ${response.results.length === 1 ? "sink" : "sinks"}.`);
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
  const toast = useToast();
  const backups: Backup[] = list.backups ?? [];
  return <section className="panel stack">
    <div className="heading"><h2>Backups</h2></div>
    <form className="stack" onSubmit={(event) => form.submit(event, async () => {
      const result = await api<{ path: string; bytes: number }>("backup", "POST", {});
      toast(`Backup created: ${result.path.split("/").pop()}.`);
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
  const latest = about?.backups?.latest_at;
  const rows: [string, ReactNode][] = [
    ["Version", about?.version ?? version ?? "—"],
    ["Started", formatTime(about?.started_at)],
    ["Uptime", about?.uptime_seconds == null ? "—" : uptime(about.uptime_seconds)],
    ["Backend", about?.backend ?? "—"],
    ["Database size", about?.db_bytes == null ? "—" : formatSize(about.db_bytes)],
    ["Write-ahead log", about?.wal_bytes == null ? "—" : formatSize(about.wal_bytes)],
    ["Backups", about?.backups ? `${about.backups.count} kept` : "—"],
    ["Latest backup", latest
      ? <>{formatTime(latest)} <span className={`pill ${about?.backups?.latest_verified ? "active" : "revoked"}`}>{about?.backups?.latest_verified ? "verified" : "failed"}</span></>
      : "—"],
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
  const [range, setRange] = useState("24h");
  const digest = useResource(`digest/preview?window=${range}`, 0, optionalResource<DigestPreview>);
  const [selected, setSelected] = useState("charter");

  const tabs = [
    { id: "charter", name: "Charter", show: true },
    { id: "capacity", name: "Capacity and limits", show: true },
    { id: "repositories", name: "Repositories", show: true },
    { id: "instructions", name: "Instructions", show: true },
    { id: "playbooks", name: "Playbooks", show: true },
    { id: "announcements", name: "Announcements", show: true },
    { id: "schedules", name: "Schedules", show: true },
    { id: "notifications", name: "Notifications", show: notify.data !== null },
    { id: "backups", name: "Backups", show: backups.data !== null },
    { id: "digest", name: "Digest", show: digest.data !== null },
    { id: "tokens", name: "Tokens", show: tokens.data !== null },
    { id: "alerts", name: "Alerts", show: alerts.data !== null },
    { id: "retention", name: "Retention", show: retention.data !== null },
    { id: "webhooks", name: "Webhooks", show: webhooks.data !== null },
    { id: "breakers", name: "Provider breakers", show: breakers.data !== null },
    { id: "about", name: "About", show: true },
  ].filter((tab) => tab.show);
  const active = tabs.some((tab) => tab.id === selected) ? selected : "charter";

  return <>
    <PageHeading title="Settings" />
    <div className="actions" role="group" aria-label="Settings">
      {tabs.map((tab) => <button key={tab.id} className="text-button" aria-pressed={tab.id === active}
        onClick={() => setSelected(tab.id)}>{tab.name}</button>)}
    </div>
    {active === "charter" && <CharterView embedded />}
    {active === "capacity" && <>
      <CapacityView revision={revisions.capacity} embedded />
      <OrgBudget revision={revisions.capacity} />
    </>}
    {active === "repositories" && <RepositoriesView />}
    {active === "instructions" && <InstructionsView embedded />}
    {active === "playbooks" && <PlaybooksPanel />}
    {active === "announcements" && <AnnouncementsView embedded />}
    {active === "schedules" && <SchedulesView embedded />}
    {active === "notifications" && <>
      <ResourceState error={notify.error} loading={notify.data === undefined} retry={notify.refresh} rows={2} />
      {notify.data && <NotificationsPanel status={notify.data} />}
      <PushPanel />
    </>}
    {active === "backups" && <>
      <ResourceState error={backups.error} loading={backups.data === undefined} retry={backups.refresh} rows={3} />
      {backups.data && <BackupsPanel list={backups.data} refresh={backups.refresh} />}
    </>}
    {active === "digest" && <>
      <ResourceState error={digest.error} loading={digest.data === undefined} retry={digest.refresh} rows={4} />
      {digest.data && <DigestPanel preview={digest.data} range={range} setRange={setRange} />}
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
