import { plainText } from "./ansi.ts";
import type { LatestOutput, OutputLine } from "./output-types";

export const rowHeight = 18;
export const viewHeight = 360;
export const overscan = 12;
export const retained = 5000;

export function retain(current: OutputLine[], batch: OutputLine[]) {
  const next = current.concat(batch);
  return next.length > retained ? next.slice(next.length - retained) : next;
}

export function freshLines(incoming: OutputLine[], last: number, taskID?: string) {
  return incoming.filter((line) => line.seq > last && (!taskID || line.task_id === taskID));
}

export function matchLines(lines: OutputLine[], query: string) {
  return query ? lines.filter((line) => plainText(line.text).toLowerCase().includes(query)) : lines;
}

export function visibleRange(scrollTop: number, total: number) {
  return {
    start: Math.max(0, Math.floor(scrollTop / rowHeight) - overscan),
    end: Math.min(total, Math.ceil((scrollTop + viewHeight) / rowHeight) + overscan),
  };
}

export function atEnd(scrollHeight: number, scrollTop: number, clientHeight: number) {
  return scrollHeight - scrollTop - clientHeight < rowHeight * 2;
}

export const newest = (a: LatestOutput, b: LatestOutput) =>
  Date.parse(b.updated_at) - Date.parse(a.updated_at) || a.agent_id.localeCompare(b.agent_id);

export function mergeLatest(current: LatestOutput[], updates: LatestOutput[]) {
  const byAgent = new Map(current.map((entry) => [entry.agent_id, entry]));
  updates.forEach((entry) => byAgent.set(entry.agent_id, { ...entry, lines: (byAgent.get(entry.agent_id)?.lines ?? 0) + entry.lines }));
  return [...byAgent.values()].sort(newest);
}
