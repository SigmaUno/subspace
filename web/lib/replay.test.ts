import assert from "node:assert/strict";
import { test } from "node:test";
import { changedTasks, clampStep, nearest, seekIndex, stack, strip, stripHigh } from "./replay.ts";
import type { ReplayPoint, ReplaySnapshot, ReplayTask } from "./replay-types";

function task(id: string, fields: Partial<ReplayTask> = {}): ReplayTask {
  return {
    id, title: id, role: "worker", status: "ready", assignee: null, reviewer: null, progress_pct: 0, cost_used: 0,
    changed_audit_id: 0, changed_at: null, ...fields,
  };
}

function snapshot(columns: Record<string, (ReplayTask | null)[] | null>): ReplaySnapshot {
  return { brief_id: "B-1", audit_id: 1, at: null, events: 0, total: 0, counts: null, columns: columns as ReplaySnapshot["columns"], agents: null };
}

const point = (audit_id: number, counts: Record<string, number> | null = null): ReplayPoint => ({ audit_id, at: "", counts });

test("only tasks that differ from the previous frame are marked changed", () => {
  const previous = snapshot({
    ready: [task("T-1"), task("T-2")],
    in_progress: [task("T-3", { assignee: "a", progress_pct: 10 })],
    done: null,
  });
  const current = snapshot({
    ready: [task("T-1")],
    in_progress: [task("T-2", { status: "in_progress" }), task("T-3", { assignee: "a", progress_pct: 10, title: "renamed", changed_at: "x" })],
    done: [task("T-4")],
  });
  assert.deepEqual([...changedTasks(current, previous)].sort(), ["T-2", "T-4"]);
});

test("each tracked field marks a task changed", () => {
  const base = task("T-1", { assignee: "a", reviewer: "r", progress_pct: 5, cost_used: 1 });
  const variants: Partial<ReplayTask>[] = [{ status: "done" }, { assignee: "b" }, { reviewer: null }, { progress_pct: 6 }, { cost_used: 1.5 }];
  for (const change of variants) {
    assert.deepEqual([...changedTasks(snapshot({ ready: [{ ...base, ...change }] }), snapshot({ ready: [base] }))], ["T-1"], JSON.stringify(change));
  }
});

test("the first frame has nothing to compare with", () => {
  assert.equal(changedTasks(snapshot({ ready: [task("T-1")] }), undefined).size, 0);
  assert.equal(changedTasks(undefined, snapshot({})).size, 0);
});

test("the nearest point is the first at or after the audit id", () => {
  const points = [point(10), point(20), point(30)];
  assert.equal(nearest(points, 1), 0);
  assert.equal(nearest(points, 10), 0);
  assert.equal(nearest(points, 11), 1);
  assert.equal(nearest(points, 30), 2);
  assert.equal(nearest(points, 31), 2);
  assert.equal(nearest(points, Number.MAX_SAFE_INTEGER), 2);
});

test("stacked bars follow board order from the bottom and skip empty statuses", () => {
  const bars = stack({ done: 2, backlog: 1, ready: 0, blocked: 1, ignored: 9 }, 4, 62);
  assert.deepEqual(bars.map((bar) => bar.status), ["backlog", "done", "blocked"]);
  assert.deepEqual(bars.map((bar) => bar.height), [15.5, 31, 15.5]);
  assert.equal(bars[0].y, strip.height - 15.5);
  for (let i = 1; i < bars.length; i++) assert.equal(bars[i].y + bars[i].height, bars[i - 1].y);
  assert.deepEqual(stack(null, 4, 62), []);
});

test("the chart scale is the busiest frame and never below one", () => {
  assert.equal(stripHigh([]), 1);
  assert.equal(stripHigh([point(1, null), point(2, {})]), 1);
  assert.equal(stripHigh([point(1, { ready: 2 }), point(2, { ready: 3, done: 4 }), point(3, { done: 1 })]), 7);
});

test("clicking the strip maps a pixel to a frame within range", () => {
  assert.equal(seekIndex(0, 1000, 10), 0);
  assert.equal(seekIndex(99, 1000, 10), 0);
  assert.equal(seekIndex(100, 1000, 10), 1);
  assert.equal(seekIndex(999.9, 1000, 10), 9);
  assert.equal(seekIndex(-50, 1000, 10), 0);
  assert.equal(seekIndex(5000, 1000, 10), 9);
  assert.equal(seekIndex(500, 1000, 1), 0);
});

test("stepping clamps to the timeline", () => {
  assert.equal(clampStep(-1, 5), 0);
  assert.equal(clampStep(3, 5), 3);
  assert.equal(clampStep(6, 5), 5);
  assert.equal(clampStep(1, -1), 0);
});
