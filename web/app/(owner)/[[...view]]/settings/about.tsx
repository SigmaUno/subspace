"use client";

import type { ReactNode } from "react";
import { uptime } from "../../../../lib/duration";
import type { About } from "../../../../lib/settings-types";
import { formatSize, formatTime } from "../shared/format";

export function AboutPanel({ version, about }: { version?: string; about?: About | null }) {
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
