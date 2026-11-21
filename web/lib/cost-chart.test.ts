import assert from "node:assert/strict";
import { test } from "node:test";
import { barParts, barWidth, chart, chartScale, color, maxBars, share, shorten, sinceSlot, slotMs, stackColors, stackFor, stackKeys } from "./cost-chart.ts";
import type { CostGroup } from "./costs-types";

function group(key: string, cost_eur: number, split?: [string, number][]): CostGroup {
  const part = (name: string, cost: number) => ({ key: name, cost_eur: cost, requests: 0, tokens_in: 0, tokens_out: 0, cache_read: 0, cache_write: 0 });
  return { ...part(key, cost_eur), split: split?.map(([name, cost]) => part(name, cost)) };
}

test("the since parameter only changes once per slot", () => {
  const day = 86_400_000;
  const base = Date.parse("2026-11-21T10:00:00Z");
  assert.equal(sinceSlot(base, day), "2026-11-20T10:00:00.000Z");
  assert.equal(sinceSlot(base + slotMs - 1, day), "2026-11-20T10:00:00.000Z");
  assert.equal(sinceSlot(base + slotMs, day), "2026-11-20T10:05:00.000Z");
  assert.equal(sinceSlot(base - 1, day), "2026-11-20T09:55:00.000Z");
});

test("providers are split by model and everything else by provider", () => {
  assert.equal(stackFor("provider"), "model");
  for (const by of ["brief", "task", "component", "agent", "kind", "model", "role", "day"] as const) assert.equal(stackFor(by), "provider");
});

test("labels are shortened to twenty characters", () => {
  assert.equal(shorten("a".repeat(20)), "a".repeat(20));
  assert.equal(shorten("a".repeat(21)), `${"a".repeat(19)}…`);
  assert.equal(shorten(""), "");
});

test("shares never divide by zero", () => {
  assert.equal(share(0, 0), 0);
  assert.equal(share(1, 3), 33);
  assert.equal(share(2, 3), 67);
  assert.equal(share(1, 200), 1);
  assert.equal(share(10, 10), 100);
});

test("series colours are deterministic and wrap the hue", () => {
  assert.equal(color(0), "hsl(210 55% 48%)");
  assert.equal(color(3), "hsl(351 55% 48%)");
  assert.equal(color(4), "hsl(38 55% 48%)");
  assert.equal(new Set(Array.from({ length: 12 }, (_, i) => color(i))).size, 12);
});

test("stack keys come from the visible bars only", () => {
  const groups = Array.from({ length: maxBars + 1 }, (_, i) => group(`g${i}`, 1, [[i === maxBars ? "hidden" : "b", 1], ["a", 1]]));
  assert.deepEqual(stackKeys(groups), ["a", "b"]);
  assert.deepEqual(stackKeys([group("x", 1), group("y", 1, [])]), []);
  const colors = stackColors(["a", "b"]);
  assert.deepEqual([...colors], [["a", color(0)], ["b", color(1)]]);
});

test("the chart shows at most twelve bars scaled to the largest", () => {
  const empty = chartScale([]);
  assert.deepEqual(empty, { shown: [], high: 1, height: chart.top + chart.bottom });
  const many = Array.from({ length: 20 }, (_, i) => group(`g${i}`, i === 15 ? 100 : i + 1));
  const scale = chartScale(many);
  assert.equal(scale.shown.length, maxBars);
  assert.equal(scale.high, 12);
  assert.equal(scale.height, chart.top + maxBars * chart.row + chart.bottom);
  assert.equal(chartScale([group("free", 0)]).high, 1);
});

test("split bars are laid out end to end and proportional", () => {
  const bar = barParts(group("g", 10, [["a", 5], ["b", 2.5], ["c", 0]]), 10);
  assert.deepEqual(bar.parts.map((part) => [part.key, part.x, part.width]), [
    ["a", chart.left, barWidth / 2], ["b", chart.left + barWidth / 2, barWidth / 4], ["c", chart.left + barWidth * 0.75, 0],
  ]);
  assert.equal(bar.end, chart.left + barWidth * 0.75);
});

test("a group without a split is one unlabelled bar", () => {
  const bar = barParts(group("g", 4), 8);
  assert.deepEqual(bar.parts.map((part) => [part.key, part.cost_eur, part.x, part.width]), [["", 4, chart.left, barWidth / 2]]);
  assert.equal(barParts(group("g", 4, []), 8).parts.length, 1);
  assert.equal(bar.end, chart.left + barWidth / 2);
});

test("the largest bar fills the available width", () => {
  assert.equal(barParts(group("g", 7), 7).end, chart.left + barWidth);
  assert.equal(barParts(group("g", -1), 7).parts[0].width, 0);
});
