"use client";

import { safeURL } from "../../../../lib/client";
import type { HillclimbPoint } from "../../../../lib/hillclimb-types";
import { VirtualList } from "../shared/virtual";

export const formatValue = (amount: number) => Number(amount.toPrecision(6)).toString();

export function AttemptList({ points }: { points: HillclimbPoint[] }) {
  if (!points.length) return null;
  return <VirtualList name="Hillclimb attempts" items={points.slice().reverse().map((point) => ({ ...point, id: point.attempt_id }))} estimate={40} className="attempt-list"
    render={(point) => {
      const href = safeURL(point.branch_url ?? null);
      return <div className="attempt-row">
        <span>#{point.n}</span>
        <span>{point.value === null ? "Failed" : formatValue(point.value)}</span>
        <span>{href ? <a href={href} target="_blank" rel="noreferrer">{point.branch}</a> : point.branch}<span className="meta"> · {point.agent_id}</span></span>
        <time className="meta" dateTime={point.measured_at}>{new Date(point.measured_at).toLocaleString()}</time>
      </div>;
    }} />;
}
