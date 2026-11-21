import assert from "node:assert/strict";
import { test } from "node:test";
import { decisionThread, diffURL, orderDecisions, pickDecision } from "./decision-queue.ts";
import type { Decision } from "./swarm";

function decision(id: string, fields: Partial<Decision> = {}): Decision {
  return { id, kind: "plan", title: id, created_at: "2026-11-21T10:00:00Z", ...fields };
}

const ids = (items: readonly Decision[]) => items.map((item) => item.id);

test("higher impact first, then oldest first", () => {
  const input = [
    decision("old-low", { impact: 1, created_at: "2026-11-21T08:00:00Z" }),
    decision("new-high", { impact: 9, created_at: "2026-11-21T11:00:00Z" }),
    decision("old-high", { impact: 9, created_at: "2026-11-21T09:00:00Z" }),
    decision("none-a", { created_at: "2026-11-21T07:00:00Z" }),
    decision("negative", { impact: -2, created_at: "2026-11-21T06:00:00Z" }),
    decision("zero", { impact: 0, created_at: "2026-11-21T07:30:00Z" }),
  ];
  assert.deepEqual(ids(orderDecisions(input, [])), ["old-high", "new-high", "old-low", "none-a", "zero", "negative"]);
});

test("ordering is stable for identical keys and leaves the feed untouched", () => {
  const input = [decision("b"), decision("a"), decision("c")];
  const snapshot = ids(input);
  assert.deepEqual(ids(orderDecisions(input, [])), ["b", "a", "c"]);
  assert.deepEqual(ids(input), snapshot);
});

test("resolved decisions are dropped", () => {
  const input = [decision("a", { impact: 5 }), decision("b", { impact: 4 }), decision("c")];
  assert.deepEqual(ids(orderDecisions(input, ["a", "ghost"])), ["b", "c"]);
  assert.deepEqual(orderDecisions([], ["a"]), []);
});

test("the selected decision stays while it is eligible", () => {
  const eligible = [decision("a"), decision("b")];
  assert.equal(pickDecision(eligible, "b", decision("a"), [], [])?.id, "b");
});

test("the server suggestion wins over the queue head unless resolved or skipped", () => {
  const eligible = [decision("a"), decision("b")];
  const suggestion = decision("s");
  assert.equal(pickDecision(eligible, undefined, suggestion, [], [])?.id, "s");
  assert.equal(pickDecision(eligible, "gone", suggestion, [], [])?.id, "s");
  assert.equal(pickDecision(eligible, undefined, suggestion, ["s"], [])?.id, "a");
  assert.equal(pickDecision(eligible, undefined, suggestion, [], ["s"])?.id, "a");
  assert.equal(pickDecision(eligible, undefined, null, [], [])?.id, "a");
  assert.equal(pickDecision([], undefined, undefined, [], []), undefined);
});

test("a decision opens the most specific thread", () => {
  assert.deepEqual(decisionThread({ task_id: "T-1", agent_id: "a", brief_id: "B-1" }), { kind: "task", id: "T-1" });
  assert.deepEqual(decisionThread({ agent_id: "a", brief_id: "B-1" }), { kind: "agent", id: "a" });
  assert.deepEqual(decisionThread({ brief_id: "B-1" }), { kind: "brief", id: "B-1" });
  assert.deepEqual(decisionThread({ task_id: "", agent_id: "", brief_id: "B-1" }), { kind: "brief", id: "B-1" });
  assert.equal(decisionThread({}), null);
});

test("pull request links open the diff", () => {
  const cases: [string, string][] = [
    ["https://github.com/o/r/pull/12", "https://github.com/o/r/pull/12/files"],
    ["https://github.com/o/r/pull/12/", "https://github.com/o/r/pull/12/files"],
    ["https://forge.example.com/o/r/pulls/3", "https://forge.example.com/o/r/pulls/3/files"],
    ["https://github.com/o/r/pull/12?x=1#c", "https://github.com/o/r/pull/12/files?x=1#c"],
    ["https://github.com/o/r/pull/12/files", "https://github.com/o/r/pull/12/files"],
    ["https://github.com/o/r/pull/abc", "https://github.com/o/r/pull/abc"],
    ["https://github.com/o/r", "https://github.com/o/r"],
  ];
  for (const [input, want] of cases) assert.equal(diffURL(input)?.href, want, input);
});

test("unsafe or missing pull request links are not rendered", () => {
  for (const input of [undefined, "", "javascript:alert(1)", "data:text/html,x", "not a url", "ftp://example.com/pull/1"]) {
    assert.equal(diffURL(input), null, String(input));
  }
});
