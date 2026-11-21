"use client";

import Link from "next/link";
import { useResource } from "../../../../lib/client";
import type { Capacity } from "../../../../lib/types";
import { EmptyState, ResourceState } from "../shared/states";
import { VirtualList } from "../shared/virtual";

export function CapacitySummary({ revision }: { revision: number }) {
  const capacity = useResource<Capacity>("capacity", revision);
  const items = (capacity.data?.limits ?? []).map((item) => ({ ...item, id: `${item.provider}:${item.model}` }));
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
  </section>;
}
