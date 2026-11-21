"use client";

import { useState } from "react";
import { api } from "../../../lib/client";
import type { Breaker, BreakerList } from "../../../lib/settings-types";
import { formatTime } from "./admin-shared";
import { Feedback } from "./components";
import { EmptyState } from "./states";

const stateClass = { closed: "active", open: "revoked", half_open: "paused" } as const;

export function BreakersPanel({ list, refresh }: { list: BreakerList; refresh: () => void }) {
  const breakers: Breaker[] = list.breakers ?? [];
  const [resetting, setResetting] = useState("");
  const [error, setError] = useState("");

  async function reset(provider: string) {
    if (!window.confirm(`Close every breaker of ${provider} and clear its samples?`)) return;
    setResetting(provider);
    setError("");
    try {
      await api<BreakerList>(`breakers/${encodeURIComponent(provider)}/reset`, "POST", {});
      refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Reset failed.");
    } finally {
      setResetting("");
    }
  }

  return <section className="panel stack">
    <div className="heading"><h2>Provider breakers</h2></div>
    <p className="meta">An open breaker stops new spawns and autopilot assignments for that provider and model. Running agents continue.</p>
    <Feedback error={error} />
    {!breakers.length && <EmptyState>No provider has reported usage yet.</EmptyState>}
    {!!breakers.length && <div className="table-wrap">
      <table>
        <thead><tr><th>Provider</th><th>Model</th><th>State</th><th>Error rate</th><th>Errors</th><th>Opened</th><th><span className="meta">Action</span></th></tr></thead>
        <tbody>{breakers.map((breaker) => <tr key={`${breaker.provider}:${breaker.model}`}>
          <th scope="row">{breaker.provider}</th>
          <td>{breaker.model}</td>
          <td><span className={`pill ${stateClass[breaker.state]}`}>{breaker.state.replace("_", " ")}</span></td>
          <td>{Math.round(breaker.error_rate * 100)}% <span className="meta">of {Math.round(breaker.threshold * 100)}% limit</span></td>
          <td>{breaker.errors} / {breaker.samples}</td>
          <td>{formatTime(breaker.opened_at)}</td>
          <td><button type="button" data-write disabled={resetting === breaker.provider} onClick={() => reset(breaker.provider)}
            aria-label={`Reset breakers of ${breaker.provider}`}>Reset</button></td>
        </tr>)}</tbody>
      </table>
    </div>}
  </section>;
}
