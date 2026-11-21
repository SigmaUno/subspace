import assert from "node:assert/strict";
import { test } from "node:test";
import { emptyQuery, formatQuery, parseQuery, type BoardQuery } from "./board-query.ts";
import { parseList, parseViews, removeView, saveView, toggleItem } from "./saved-views.ts";

test("stored lists tolerate any garbage", () => {
  assert.deepEqual(parseList(null), []);
  assert.deepEqual(parseList(""), []);
  assert.deepEqual(parseList("{"), []);
  assert.deepEqual(parseList('{"a":1}'), []);
  assert.deepEqual(parseList('"blocked"'), []);
  assert.deepEqual(parseList('["a",1,null,{"x":1},"b",["c"]]'), ["a", "b"]);
});

test("stored views drop malformed entries and keep order", () => {
  assert.deepEqual(parseViews(null), []);
  assert.deepEqual(parseViews("not json"), []);
  assert.deepEqual(parseViews('{"name":"a","query":"q"}'), []);
  assert.deepEqual(
    parseViews('[{"name":"a","query":"q=1"},null,5,"x",{"name":"b"},{"query":"z"},{"name":1,"query":"z"},{"name":"c","query":"","extra":true}]'),
    [{ name: "a", query: "q=1" }, { name: "c", query: "", extra: true }],
  );
});

test("saving a view under an existing name replaces it and moves it last", () => {
  const views = [{ name: "a", query: "1" }, { name: "b", query: "2" }];
  assert.deepEqual(saveView(views, "a", "3"), [{ name: "b", query: "2" }, { name: "a", query: "3" }]);
  assert.deepEqual(saveView(views, "c", "4"), [...views, { name: "c", query: "4" }]);
  assert.deepEqual(views, [{ name: "a", query: "1" }, { name: "b", query: "2" }]);
});

test("removing a view that does not exist changes nothing", () => {
  const views = [{ name: "a", query: "1" }, { name: "b", query: "2" }];
  assert.deepEqual(removeView(views, "a"), [{ name: "b", query: "2" }]);
  assert.deepEqual(removeView(views, "zzz"), views);
});

test("collapsed columns toggle", () => {
  assert.deepEqual(toggleItem(new Set(["done"]), "blocked"), ["done", "blocked"]);
  assert.deepEqual(toggleItem(new Set(["done", "blocked"]), "done"), ["blocked"]);
  assert.deepEqual(toggleItem(new Set(), "done"), ["done"]);
});

test("a saved view restores the exact filters through storage", () => {
  const query: BoardQuery = {
    ...emptyQuery, text: "a&b=c %20 ü 😀 #x", assignee: "-", role: "tester", group: "review", priority: "-3", blocked: true,
  };
  const stored = JSON.stringify(saveView([], "mine", formatQuery(query)));
  const [view] = parseViews(stored);
  assert.deepEqual(parseQuery(view.query), query);
});
