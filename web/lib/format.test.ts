import assert from "node:assert/strict";
import { test } from "node:test";
import { formatSize } from "./format.ts";

test("sizes scale by 1024 and stop at terabytes", () => {
  const cases: [number, string][] = [
    [0, "0 B"], [1023, "1023 B"], [1024, "1.0 KB"], [1536, "1.5 KB"], [1024 ** 2, "1.0 MB"],
    [1024 ** 3 * 2.25, "2.3 GB"], [1024 ** 4, "1.0 TB"], [1024 ** 5, "1024.0 TB"],
  ];
  for (const [bytes, want] of cases) assert.equal(formatSize(bytes), want, String(bytes));
});
