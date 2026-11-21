import type { CostDimension, CostGroup } from "./costs-types";

export const slotMs = 300_000;
export const maxBars = 12;
export const chart = { width: 720, row: 28, left: 150, right: 80, top: 8, bottom: 8 };
export const barWidth = chart.width - chart.left - chart.right;

export const stackFor = (by: CostDimension): CostDimension => by === "provider" ? "model" : "provider";
export const shorten = (text: string) => text.length > 20 ? `${text.slice(0, 19)}…` : text;
export const color = (index: number) => `hsl(${(index * 47 + 210) % 360} 55% 48%)`;

export function sinceSlot(now: number, rangeMs: number) {
  return new Date(Math.floor((now - rangeMs) / slotMs) * slotMs).toISOString();
}

export function stackKeys(groups: CostGroup[]) {
  return [...new Set(groups.slice(0, maxBars).flatMap((group) => (group.split ?? []).map((part) => part.key)))].sort();
}

export function stackColors(keys: string[]) {
  return new Map<string, string>(keys.map((key, index): [string, string] => [key, color(index)]));
}

export function share(cost: number, total: number) {
  return Math.round(cost / (total || 1) * 100);
}

export function barParts(group: CostGroup, high: number) {
  const parts: { key: string; cost_eur: number }[] = group.split?.length ? group.split : [{ key: "", cost_eur: group.cost_eur }];
  let offset = 0;
  return {
    parts: parts.map((part) => {
      const width = part.cost_eur / high * barWidth;
      const x = chart.left + offset;
      offset += width;
      return { key: part.key, cost_eur: part.cost_eur, x, width: Math.max(width, 0) };
    }),
    end: chart.left + offset,
  };
}

export function chartScale(groups: CostGroup[]) {
  const shown = groups.slice(0, maxBars);
  const high = Math.max(...shown.map((group) => group.cost_eur), 0) || 1;
  return { shown, high, height: chart.top + shown.length * chart.row + chart.bottom };
}
