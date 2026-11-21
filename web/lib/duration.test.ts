import assert from "node:assert/strict";
import { test } from "node:test";
import { agentDuration, elapsedMinutes, estimateMinutes, forecastMinutes, reportDuration, spawnSpan, uptime } from "./duration.ts";

function table(fn: (input: number) => string, cases: [number, string][]) {
  for (const [input, want] of cases) assert.equal(fn(input), want, `${fn.name}(${input})`);
}

test("estimate minutes switch to hours at the rounded hour", () => {
  table(estimateMinutes, [[0, "0 min"], [59.4, "59 min"], [59.5, "1 h 0 min"], [125, "2 h 5 min"], [1500, "25 h 0 min"]]);
});

test("forecast minutes become days after two days", () => {
  table(forecastMinutes, [[0, "0m"], [59, "59m"], [60, "1h 0m"], [47 * 60 + 59, "47h 59m"], [48 * 60, "2d"], [71 * 60, "3d"], [24 * 60 * 30, "30d"]]);
});

test("agent durations", () => {
  table(agentDuration, [[0, "—"], [1, "1s"], [89, "89s"], [90, "2m"], [5399, "90m"], [5400, "1.5h"], [36000, "10.0h"]]);
});

test("report durations", () => {
  table(reportDuration, [[0, "0s"], [59, "59s"], [60, "1m"], [3599, "1h 0m"], [3600, "1h 0m"], [47 * 3600 + 59 * 60, "47h 59m"], [48 * 3600, "2d 0h"], [100 * 3600, "4d 4h"]]);
});

test("uptime keeps every unit below the largest non-zero one", () => {
  table(uptime, [[0, "0m 0s"], [59.9, "0m 59s"], [3661, "1h 1m"], [86400, "1d 0h 0m"], [90061, "1d 1h 1m"]]);
});

test("elapsed minutes floor", () => {
  table(elapsedMinutes, [[0, "0m"], [59.9, "59m"], [60, "1h 0m"], [125.7, "2h 5m"]]);
});

test("spawn spans never go negative", () => {
  table(spawnSpan, [[-5000, "<1m"], [59_999, "<1m"], [60_000, "1m"], [3_599_999, "59m"], [3_600_000, "1h 0m"], [86_400_000, "1d 0h"], [90_000_000, "1d 1h"]]);
});
