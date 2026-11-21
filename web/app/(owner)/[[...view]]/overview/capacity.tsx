"use client";

import Link from "next/link";
import { useResource } from "../../../../lib/client";
import type { Capacity, ProviderProbe } from "../../../../lib/types";
import { EmptyState, ResourceState } from "../shared/states";
import { VirtualList } from "../shared/virtual";

function probeLabel(probe: ProviderProbe) {
  if (!probe.checked_at) return "not checked yet";
  const at = new Date(probe.checked_at).toLocaleTimeString([], { timeStyle: "short" });
  return probe.ok ? `${probe.latency_ms} ms · checked ${at}` : `${probe.error ?? "unavailable"} · checked ${at}`;
}

export function CapacitySummary({ revision }: { revision: number }) {
  const capacity = useResource<Capacity>("capacity", revision);
  const providers = useResource<{ providers: ProviderProbe[] | null }>("providers", revision);
  const items = (capacity.data?.limits ?? []).map((item) => ({ ...item, id: `${item.provider}:${item.model}` }));
  const probes = providers.data?.providers ?? [];
  return <section className="panel stack">
    <div className="heading"><h2>Inference capacity</h2><Link href="/capacity">Settings</Link></div>
    <ResourceState error={capacity.error} loading={!capacity.data} retry={capacity.refresh} rows={2} />
    {capacity.data && <p className="meta">{capacity.data.agents_in_use} / {capacity.data.global_max_agents} agents in use</p>}
    {items.length ? <VirtualList name="Provider and model capacity" estimate={56} items={items} className="capacity-list"
      render={(item) => <div className="capacity-usage">
        <strong>{item.provider} · {item.model}</strong>
        <progress max={Math.max(1, item.max_concurrency)} value={item.in_use} aria-label={`${item.provider} ${item.model} in use`} />
        <span className={item.available <= 0 ? "stale" : "meta"}>{item.in_use} / {item.max_concurrency} · {item.available} free</span>
      </div>} /> : capacity.data && <EmptyState>No provider or model limits.</EmptyState>}
    {!!probes.length && <ul className="stack" style={{ listStyle: "none", padding: 0, margin: 0 }} aria-label="Provider availability">
      {probes.map((probe) => <li key={`${probe.provider}:${probe.model}`} className="capacity-usage">
        <strong>{probe.provider} · {probe.model}</strong>
        <span className={`pill ${probe.ok ? "active" : probe.checked_at ? "revoked" : ""}`}>{probe.ok ? "available" : probe.checked_at ? "unavailable" : "unknown"}</span>
        <span className={probe.ok ? "meta" : "stale"}>{probeLabel(probe)}{probe.breaker !== "closed" ? ` · breaker ${probe.breaker.replace("_", "-")}` : ""}</span>
      </li>)}
    </ul>}
  </section>;
}
