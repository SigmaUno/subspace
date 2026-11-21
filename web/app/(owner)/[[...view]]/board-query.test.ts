import assert from "node:assert/strict";
import { test } from "node:test";
import { emptyQuery, filterTasks, formatQuery, haystack, parseQuery, type BoardQuery } from "./board-query.ts";
import type { Task } from "../../../lib/types";

function task(fields: Partial<Task> & { id: string }): Task {
  return {
    title: "", spec: "", acceptance_criteria: null, repo: "o/r", branch: "", role: "worker", assignee: null,
    depends_on: null, status: "backlog", progress_pct: 0, last_heartbeat_at: null, lease_expires_at: null,
    pr_url: null, budget: { max_minutes: 0, max_cost_eur: 0 }, cost_used: 0, minutes_used: 0, ...fields,
  } as Task;
}

const tasks = [
  task({ id: "T-1", title: "Login", status: "in_progress", assignee: "claude-1", priority: 2, component_id: "C-1" }),
  task({ id: "T-2", title: "Docs", spec: "write the webhook guide", status: "ready", role: "tester", priority: 1 }),
  task({ id: "T-3", title: "Schema", status: "blocked", assignee: "codex-1", priority: 2, component_id: "" }),
];
const haystacks = new Map(tasks.map((item) => [item.id, haystack(item)]));
const matcher = { isStale: (item: Task) => item.id === "T-1", isMergeReady: (id: string) => id === "T-2" };

function ids(query: BoardQuery) {
  return filterTasks(tasks, haystacks, query, matcher).map((item) => item.id);
}

test("a view survives the URL round trip", () => {
  const query: BoardQuery = {
    text: "login flow", assignee: "claude-1", role: "worker", group: "active", component: "C-1", priority: "2",
    blocked: true, stale: true, mergeReady: true,
  };
  assert.deepEqual(parseQuery(formatQuery(query)), query);
  assert.equal(formatQuery(emptyQuery), "");
});

test("invalid URL values fall back to no filter", () => {
  assert.deepEqual(parseQuery("group=bogus&priority=x1"), emptyQuery);
});

test("filters combine", () => {
  assert.deepEqual(ids(emptyQuery), ["T-1", "T-2", "T-3"]);
  assert.deepEqual(ids({ ...emptyQuery, group: "attention" }), ["T-3"]);
  assert.deepEqual(ids({ ...emptyQuery, assignee: "-" }), ["T-2"]);
  assert.deepEqual(ids({ ...emptyQuery, component: "-" }), ["T-2", "T-3"]);
  assert.deepEqual(ids({ ...emptyQuery, component: "C-1", priority: "2" }), ["T-1"]);
  assert.deepEqual(ids({ ...emptyQuery, stale: true }), ["T-1"]);
  assert.deepEqual(ids({ ...emptyQuery, text: "WEBHOOK" }), ["T-2"]);
  assert.deepEqual(ids({ ...emptyQuery, mergeReady: true, role: "tester" }), ["T-2"]);
});
