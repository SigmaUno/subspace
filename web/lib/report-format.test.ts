import assert from "node:assert/strict";
import { test } from "node:test";
import { entries, markdown, segments } from "./report-format.ts";
import type { BriefReport, TaskReport } from "./report-types";

function task(fields: Partial<TaskReport>): TaskReport {
  return {
    id: "T-1", title: "title", status: "done", assignee: "a", provider: "p", cost_eur: 0, created_at: "2026-11-21T10:00:00Z",
    claimed_at: null, submitted_at: null, done_at: null, queue_seconds: 0, work_seconds: 0, review_seconds: 0, blocked_seconds: 0,
    review_rounds: 0, timeline: null, ...fields,
  } as TaskReport;
}

function report(fields: Partial<BriefReport>): BriefReport {
  return {
    brief_id: "B-1", title: "Brief", generated_at: "2026-11-21T12:00:00Z", total: 0, counts: null, tasks: null,
    cost: { total_eur: 0, by_task: null, by_agent: null, by_provider: null }, blocked_seconds: 0,
    throughput: { done: 0, window_hours: 0, per_hour: 0, hourly: null }, critical_path: { task_ids: null, seconds: 0 },
    outliers: null, ...fields,
  } as BriefReport;
}

const step = (at: string, from: string, to: string) => ({ at, from, to, by: "a", tool: "t", elapsed_seconds: 0 });
const iso = (hour: number, minute = 0) => `2026-11-21T${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}:00Z`;

test("entries sort by value then name and tolerate null", () => {
  assert.deepEqual(entries(null), []);
  assert.deepEqual(entries({ b: 1, a: 1, c: 5, d: 0 }), [["c", 5], ["a", 1], ["b", 1], ["d", 0]]);
});

test("the segments of a task follow its status timeline", () => {
  const parts = segments(task({
    created_at: iso(10),
    status: "done",
    done_at: iso(14),
    timeline: [step(iso(11), "backlog", "ready"), step(iso(12), "ready", "in_progress"), step(iso(13), "in_progress", "done")] as TaskReport["timeline"],
  }), Date.parse(iso(20)));
  assert.deepEqual(parts.map((part) => [part.status, part.from, part.to]), [
    ["backlog", Date.parse(iso(10)), Date.parse(iso(11))],
    ["ready", Date.parse(iso(11)), Date.parse(iso(12))],
    ["in_progress", Date.parse(iso(12)), Date.parse(iso(13))],
  ]);
});

test("an open task runs until the report was generated and a finished one stops at its end", () => {
  const timeline = [step(iso(11), "backlog", "in_progress")] as TaskReport["timeline"];
  const open = segments(task({ status: "in_progress", timeline }), Date.parse(iso(12)));
  assert.deepEqual(open.at(-1), { status: "in_progress", from: Date.parse(iso(11)), to: Date.parse(iso(12)) });
  const blocked = segments(task({ status: "blocked", created_at: iso(10), done_at: iso(11, 30), timeline: [] }), Date.parse(iso(12)));
  assert.deepEqual(blocked, [{ status: "blocked", from: Date.parse(iso(10)), to: Date.parse(iso(11, 30)) }]);
  assert.deepEqual(segments(task({ status: "cancelled", timeline: [] }), Date.parse(iso(12))), []);
});

test("zero length and backwards intervals are dropped", () => {
  const parts = segments(task({
    created_at: iso(10),
    status: "ready",
    timeline: [step(iso(10), "backlog", "ready"), step(iso(9), "ready", "assigned")] as TaskReport["timeline"],
  }), Date.parse(iso(9)));
  assert.deepEqual(parts, []);
});

test("a task without a timeline falls back to its own status", () => {
  assert.deepEqual(segments(task({ status: "ready", timeline: null }), Date.parse(iso(11))), [
    { status: "ready", from: Date.parse(iso(10)), to: Date.parse(iso(11)) },
  ]);
});

test("the markdown export escapes table breaking titles", () => {
  const text = markdown(report({
    tasks: [task({ id: "T-1", title: "a | b | c", assignee: "", queue_seconds: 90, work_seconds: 7200, cost_eur: 1.5, review_rounds: 2 })],
    cost: { total_eur: 3.456, by_task: null, by_agent: null, by_provider: { openai: 1, anthropic: 2.456 } },
    blocked_seconds: 3700,
    throughput: { done: 4, window_hours: 2, per_hour: 2, hourly: null },
    critical_path: { task_ids: ["T-1", "T-2"], seconds: 600 },
  }));
  assert.match(text, /\| T-1 \| a \\\| b \\\| c \| done \| unassigned \| 2m \| 2h 0m \| 0s \| 0s \| 2 \| 1\.50 \|/);
  assert.match(text, /Total: 3\.46 €\n\n- anthropic: 2\.46 €\n- openai: 1\.00 €\n/);
  assert.match(text, /- Blocked time: 1h 2m\n/);
  assert.match(text, /- Done: 4 in 2\.00 h \(2\.00 per hour\)\n/);
  assert.ok(text.endsWith("- Critical path: T-1 → T-2 (10m)\n"));
  assert.equal(text.split("\n").filter((row) => row.startsWith("| T-")).length, 1);
});

test("an empty report has no task rows and no critical path", () => {
  const text = markdown(report({}));
  assert.ok(!text.includes("Critical path"));
  assert.ok(text.startsWith("# B-1 — Brief\n"));
  assert.ok(text.endsWith("per hour)\n"));
});
