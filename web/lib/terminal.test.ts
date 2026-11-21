import assert from "node:assert/strict";
import { test } from "node:test";
import { atEnd, freshLines, matchLines, mergeLatest, newest, overscan, retain, retained, rowHeight, viewHeight, visibleRange } from "./terminal.ts";
import type { LatestOutput, OutputLine } from "./output-types";

const esc = String.fromCharCode(27);

function line(seq: number, text = "", task_id: string | null = null): OutputLine {
  return { seq, agent_id: "a", task_id, stream: "stdout", text, ts: "2026-11-21T10:00:00Z" };
}

function latest(agent_id: string, updated_at: string, lines = 1): LatestOutput {
  return { agent_id, line: line(1, agent_id), lines, updated_at };
}

test("the scrollback keeps only the newest lines in order", () => {
  const current = Array.from({ length: retained - 2 }, (_, i) => line(i + 1));
  const batch = Array.from({ length: 5 }, (_, i) => line(retained - 1 + i));
  const next = retain(current, batch);
  assert.equal(next.length, retained);
  assert.equal(next[0].seq, 4);
  assert.equal(next[next.length - 1].seq, retained + 3);
  assert.equal(retain([line(1)], [line(2)]).length, 2);
  assert.equal(retain([], Array.from({ length: retained * 2 }, (_, i) => line(i)))[0].seq, retained);
});

test("incoming lines are limited to unseen ones for the selected task", () => {
  const incoming = [line(1), line(2, "", "T-1"), line(3, "", "T-2"), line(4, "", null)];
  assert.deepEqual(freshLines(incoming, 1).map((item) => item.seq), [2, 3, 4]);
  assert.deepEqual(freshLines(incoming, 0, "T-2").map((item) => item.seq), [3]);
  assert.deepEqual(freshLines(incoming, 4), []);
});

test("search ignores escape sequences and matches across colour changes", () => {
  const lines = [
    line(1, `${esc}[31mERR${esc}[0mOR happened`),
    line(2, `${esc}[1;31mok${esc}[0m`),
    line(3, "no match"),
  ];
  assert.deepEqual(matchLines(lines, "error").map((item) => item.seq), [1]);
  assert.deepEqual(matchLines(lines, "31m"), []);
  assert.equal(matchLines(lines, "[").length, 0);
  assert.equal(matchLines(lines, ""), lines);
});

test("only the visible rows plus overscan are rendered", () => {
  assert.deepEqual(visibleRange(0, 10), { start: 0, end: 10 });
  assert.deepEqual(visibleRange(0, 0), { start: 0, end: 0 });
  const rows = Math.ceil(viewHeight / rowHeight);
  assert.deepEqual(visibleRange(0, 1000), { start: 0, end: rows + overscan });
  assert.deepEqual(visibleRange(rowHeight * 100, 1000), { start: 100 - overscan, end: 100 + rows + overscan });
  assert.deepEqual(visibleRange(rowHeight * 100 + 1, 1000), { start: 100 - overscan, end: 100 + rows + overscan + 1 });
  assert.deepEqual(visibleRange(rowHeight * 995, 1000), { start: 995 - overscan, end: 1000 });
});

test("following resumes within two rows of the end", () => {
  assert.equal(atEnd(1000, 640, 360), true);
  assert.equal(atEnd(1000, 605, 360), true);
  assert.equal(atEnd(1000, 604, 360), false);
  assert.equal(atEnd(360, 0, 360), true);
});

test("latest output is ordered newest first with the agent id as tie breaker", () => {
  const sorted = [latest("b", "2026-11-21T10:00:00Z"), latest("c", "2026-11-21T11:00:00Z"), latest("a", "2026-11-21T10:00:00Z")].sort(newest);
  assert.deepEqual(sorted.map((entry) => entry.agent_id), ["c", "a", "b"]);
});

test("live updates replace the line and accumulate the line count", () => {
  const current = [latest("a", "2026-11-21T10:00:00Z", 5), latest("b", "2026-11-21T09:00:00Z", 2)];
  const merged = mergeLatest(current, [latest("b", "2026-11-21T12:00:00Z", 3), latest("c", "2026-11-21T11:00:00Z", 1)]);
  assert.deepEqual(merged.map((entry) => [entry.agent_id, entry.lines]), [["b", 5], ["c", 1], ["a", 5]]);
  assert.equal(current[1].lines, 2);
});
