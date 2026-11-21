import assert from "node:assert/strict";
import { test } from "node:test";
import { commaList, label, safeURL } from "./text.ts";

test("safeURL only passes http and https", () => {
  assert.equal(safeURL("https://github.com/o/r/pull/7"), "https://github.com/o/r/pull/7");
  assert.equal(safeURL("HTTP://Example.COM"), "http://example.com/");
  assert.equal(safeURL("  https://example.com/a b "), "https://example.com/a%20b");
  for (const hostile of [
    "javascript:alert(1)", "JaVaScRiPt:alert(1)", "java\nscript:alert(1)", "\tjavascript:alert(1)", "data:text/html,<script>1</script>",
    "vbscript:x", "file:///etc/passwd", "ftp://example.com", "//example.com", "/relative", "example.com", "https://", "http://[::1", "",
  ]) assert.equal(safeURL(hostile), undefined, JSON.stringify(hostile));
  assert.equal(safeURL(null), undefined);
  assert.equal(safeURL(undefined), undefined);
});

test("comma lists split on commas and newlines", () => {
  assert.deepEqual(commaList(" a, b\nc ,, \n\n,d "), ["a", "b", "c", "d"]);
  assert.deepEqual(commaList(""), []);
  assert.deepEqual(commaList(" , \n "), []);
  assert.deepEqual(commaList("a b,c"), ["a b", "c"]);
});

test("labels replace every underscore", () => {
  assert.equal(label("changes_requested"), "changes requested");
  assert.equal(label("needs_owner_now"), "needs owner now");
});
