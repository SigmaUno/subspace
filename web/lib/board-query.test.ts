import assert from "node:assert/strict";
import { test } from "node:test";
import { emptyQuery, facets, filterTasks, formatQuery, haystack, parseQuery, type BoardQuery } from "./board-query.ts";
import type { Task } from "./types";

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

test("hostile text survives the URL codec", () => {
  for (const text of ["a&assignee=evil", "100%", "%zz", "x=y#frag", "a+b c", "ünï 😀 \u202e", "\n\t", `${"long ".repeat(5000)}`]) {
    const query = { ...emptyQuery, text };
    assert.deepEqual(parseQuery(formatQuery(query)), query, JSON.stringify(text.slice(0, 20)));
  }
  assert.deepEqual(parseQuery("?q=a%26assignee%3Devil"), { ...emptyQuery, text: "a&assignee=evil" });
});

test("only well formed priorities and flags are accepted", () => {
  const priorities: [string, string][] = [["0", "0"], ["-5", "-5"], ["999999999", "999999999"], ["1000000000", ""], ["1.5", ""], ["+1", ""], ["", ""], [" 1", ""], ["1\n", ""], ["0x10", ""]];
  for (const [raw, want] of priorities) assert.equal(parseQuery(`priority=${encodeURIComponent(raw)}`).priority, want, JSON.stringify(raw));
  assert.deepEqual(parseQuery("blocked=true&stale=0&merge=yes"), emptyQuery);
  assert.equal(parseQuery("blocked=1&blocked=0").blocked, true);
  assert.equal(parseQuery("role=a&role=b").role, "a");
});

test("zero is a real priority filter", () => {
  const items = [task({ id: "T-1", priority: 0 }), task({ id: "T-2" }), task({ id: "T-3", priority: 3 })];
  const maps = new Map(items.map((item) => [item.id, haystack(item)]));
  const query = parseQuery(formatQuery({ ...emptyQuery, priority: "0" }));
  assert.equal(query.priority, "0");
  assert.deepEqual(filterTasks(items, maps, query, matcher).map((item) => item.id), ["T-1"]);
});

test("serialisation is stable and omits empty fields", () => {
  assert.equal(formatQuery({ ...emptyQuery, mergeReady: true, text: "x", blocked: true }), "q=x&blocked=1&merge=1");
  assert.equal(formatQuery(parseQuery("merge=1&stale=1&q=x")), "q=x&stale=1&merge=1");
});

test("search is case insensitive, trimmed and spans id, spec, assignee, repo and branch", () => {
  const item = task({ id: "T-9", title: "Ünï", spec: "Spec Words", assignee: "Codex-2", repo: "Org/Repo", branch: "sub/t-9-feature" });
  const lookup = new Map([[item.id, haystack(item)]]);
  for (const text of ["  t-9 ", "SPEC words", "codex-2", "org/repo", "t-9-feature", "ünï"]) {
    assert.equal(filterTasks([item], lookup, { ...emptyQuery, text }, matcher).length, 1, text);
  }
  assert.equal(filterTasks([item], lookup, { ...emptyQuery, text: "nope" }, matcher).length, 0);
  assert.equal(filterTasks([item], new Map(), { ...emptyQuery, text: "t-9" }, matcher).length, 0);
});

test("stale only counts in-progress tasks", () => {
  const items = [task({ id: "T-1", status: "in_progress" }), task({ id: "T-2", status: "blocked" })];
  const lookup = new Map(items.map((item) => [item.id, haystack(item)]));
  const all = { isStale: () => true, isMergeReady: () => false };
  assert.deepEqual(filterTasks(items, lookup, { ...emptyQuery, stale: true }, all).map((item) => item.id), ["T-1"]);
});

test("facets are sorted and de-duplicated", () => {
  assert.deepEqual(facets([
    task({ id: "a", assignee: "zed", role: "worker", priority: 1 }),
    task({ id: "b", assignee: "amy", role: "tester", priority: 0 }),
    task({ id: "c", assignee: "zed", role: "worker", priority: 5 }),
    task({ id: "d", role: "worker" }),
  ]), { assignees: ["amy", "zed"], roles: ["tester", "worker"], priorities: [5, 1, 0] });
});
