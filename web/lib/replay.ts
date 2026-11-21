import type { ReplayPoint, ReplaySnapshot, ReplayTask } from "./replay-types";
import { statuses } from "./types.ts";

export const strip = { width: 1000, height: 72, keys: 10 };

function same(a: ReplayTask, b: ReplayTask) {
  return a.status === b.status && a.assignee === b.assignee && a.reviewer === b.reviewer &&
    a.progress_pct === b.progress_pct && a.cost_used === b.cost_used;
}

export function changedTasks(current?: ReplaySnapshot, previous?: ReplaySnapshot) {
  const out = new Set<string>();
  if (!current || !previous) return out;
  const before = new Map(Object.values(previous.columns).flatMap((tasks) => tasks ?? []).map((task) => [task.id, task]));
  for (const task of Object.values(current.columns).flatMap((tasks) => tasks ?? [])) {
    const old = before.get(task.id);
    if (!old || !same(old, task)) out.add(task.id);
  }
  return out;
}

export function nearest(points: ReplayPoint[], auditID: number) {
  const index = points.findIndex((point) => point.audit_id >= auditID);
  return index < 0 ? points.length - 1 : index;
}

export function stack(counts: Record<string, number> | null, high: number, area: number) {
  const out: { status: string; y: number; height: number }[] = [];
  let y = strip.height;
  for (const [status] of statuses) {
    const n = counts?.[status] ?? 0;
    if (!n) continue;
    const height = (n / high) * area;
    y -= height;
    out.push({ status, y, height });
  }
  return out;
}

export function totalCount(counts: Record<string, number> | null) {
  return Object.values(counts ?? {}).reduce((sum, n) => sum + n, 0);
}

export function stripHigh(points: ReplayPoint[]) {
  return Math.max(1, ...points.map((point) => totalCount(point.counts)));
}

export function seekIndex(offset: number, width: number, count: number) {
  return Math.min(count - 1, Math.max(0, Math.floor((offset / width) * count)));
}

export function clampStep(next: number, last: number) {
  return Math.max(0, Math.min(last, next));
}
