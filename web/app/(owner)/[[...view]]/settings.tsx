"use client";

import { useState, type ReactNode } from "react";
import { api, optionalResource, useResource, useSubmit } from "../../../lib/client";
import type { About, Alerts, Backup, BackupList, NotifyStatus, OwnerToken, SinkResult, TokenList } from "../../../lib/settings-types";
import type { DigestPreview } from "../../../lib/digest-types";
import type { Control } from "../../../lib/types";
import { AnnouncementsView } from "./announcements";
import { CharterView } from "./charter";
import { Feedback } from "./components";
import { DigestPanel } from "./digest";
import { InstructionsView } from "./instructions";
import type { Revisions } from "./live";
import { CapacityView } from "./operations";
import { OrgBudget } from "./org-budget";
import { EmptyState, ResourceState } from "./states";

const units = ["B", "KB", "MB", "GB", "TB"];

function formatSize(bytes: number) {
  let size = bytes;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit++;
  }
  return `${unit ? size.toFixed(1) : size} ${units[unit]}`;
}

function formatDuration(seconds: number) {
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor(seconds % 86400 / 3600);
  const minutes = Math.floor(seconds % 3600 / 60);
  if (days) return `${days}d ${hours}h ${minutes}m`;
  if (hours) return `${hours}h ${minutes}m`;
  return `${minutes}m ${Math.floor(seconds % 60)}s`;
}

function formatTime(iso?: string | null) {
  return iso ? <time dateTime={iso}>{new Date(iso).toLocaleString()}</time> : "—";
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

function TokensPanel({ list }: { list: TokenList }) {
  const tokens: OwnerToken[] = list.tokens ?? [];
  return <section className="panel stack">
    <div className="heading"><h2>API tokens</h2></div>
    {!tokens.length && <EmptyState>No owner API tokens.</EmptyState>}
    {!!tokens.length && <div className="table-wrap">
      <table>
        <thead><tr><th>Name</th><th>Created</th><th>Expires</th><th>Last used</th></tr></thead>
        <tbody>{tokens.map((token, index) => (
          <tr key={token.id ?? index}>
            <th scope="row">{token.name ?? token.id ?? "—"}</th>
            <td>{formatTime(token.created_at)}</td>
            <td>{token.expires_at ? formatTime(token.expires_at) : "never"}</td>
            <td>{formatTime(token.last_used_at)}</td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
  </section>;
}

function flatten(value: unknown, prefix = ""): [string, string][] {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return Object.entries(value).flatMap(([key, item]) => flatten(item, prefix ? `${prefix}.${key}` : key));
  }
  return [[prefix, typeof value === "object" ? JSON.stringify(value) : String(value)]];
}

function AlertsPanel({ alerts }: { alerts: Alerts }) {
  const rows = flatten(alerts.thresholds ?? alerts);
  return <section className="panel stack">
    <div className="heading"><h2>Alert thresholds</h2><span className="meta">Effective values, read only</span></div>
    {!rows.length && <EmptyState>No thresholds reported.</EmptyState>}
    {!!rows.length && <div className="table-wrap">
      <table>
        <thead><tr><th>Setting</th><th>Value</th></tr></thead>
        <tbody>{rows.map(([key, text]) => <tr key={key}><th scope="row">{key}</th><td>{text}</td></tr>)}</tbody>
      </table>
    </div>}
  </section>;
}

function AboutPanel({ version, about }: { version?: string; about?: About | null }) {
  const latest = about?.backups?.latest_at;
  const rows: [string, ReactNode][] = [
    ["Version", about?.version ?? version ?? "—"],
    ["Started", formatTime(about?.started_at)],
    ["Uptime", about?.uptime_seconds === undefined ? "—" : formatDuration(about.uptime_seconds)],
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
  const alerts = useResource("alerts", revisions.control, optionalResource<Alerts>);
  const about = useResource("about", 0, optionalResource<About>);
  const [range, setRange] = useState("24h");
  const digest = useResource(`digest/preview?window=${range}`, 0, optionalResource<DigestPreview>);
  const [selected, setSelected] = useState("charter");

  const tabs = [
    { id: "charter", name: "Charter", show: true },
    { id: "capacity", name: "Capacity and limits", show: true },
    { id: "instructions", name: "Instructions", show: true },
    { id: "announcements", name: "Announcements", show: true },
    { id: "notifications", name: "Notifications", show: notify.data !== null },
    { id: "backups", name: "Backups", show: backups.data !== null },
    { id: "digest", name: "Digest", show: digest.data !== null },
    { id: "tokens", name: "Tokens", show: tokens.data !== null },
    { id: "alerts", name: "Alerts", show: alerts.data !== null },
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
    {active === "notifications" && <>
      <ResourceState error={notify.error} loading={notify.data === undefined} retry={notify.refresh} rows={2} />
      {notify.data && <NotificationsPanel status={notify.data} />}
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
      {tokens.data && <TokensPanel list={tokens.data} />}
    </>}
    {active === "alerts" && <>
      <ResourceState error={alerts.error} loading={alerts.data === undefined} retry={alerts.refresh} rows={3} />
      {alerts.data && <AlertsPanel alerts={alerts.data} />}
    </>}
    {active === "about" && <>
      <ResourceState error={state.error} loading={state.data === undefined} retry={state.refresh} rows={2} />
      <AboutPanel version={state.data?.version} about={about.data} />
    </>}
  </>;
}
