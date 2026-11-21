"use client";

import { useState } from "react";
import { api, useSubmit } from "../../../../lib/client";
import type { NotifyStatus, SinkResult } from "../../../../lib/settings-types";
import { Feedback } from "../shared/components";
import { EmptyState } from "../shared/states";
import { useToast } from "../shared/ui";

export function NotificationsPanel({ status }: { status: NotifyStatus }) {
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
