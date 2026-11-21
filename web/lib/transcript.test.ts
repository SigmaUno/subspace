import assert from "node:assert/strict";
import { test } from "node:test";
import { localTimeToRFC3339, transcriptPath } from "./transcript.ts";

test("transcript paths carry only the filters that are set", () => {
  assert.equal(transcriptPath("T-1"), "tasks/T-1/transcript?limit=500");
  assert.equal(
    transcriptPath("T-1", { since: 500, q: "  panic ", at: "2026-03-01T09:30:00Z" }),
    "tasks/T-1/transcript?limit=500&since=500&q=panic&at=2026-03-01T09%3A30%3A00Z",
  );
  assert.equal(transcriptPath("T-1", { since: 0, q: "   " }), "tasks/T-1/transcript?limit=500");
});

test("a local date and time becomes an RFC 3339 instant the server accepts", () => {
  const instant = localTimeToRFC3339("2026-03-01T09:30");
  assert.match(instant ?? "", /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:00Z$/);
  assert.equal(Date.parse(instant ?? ""), new Date(2026, 2, 1, 9, 30).getTime());
  assert.equal(localTimeToRFC3339(""), undefined);
  assert.equal(localTimeToRFC3339("not a time"), undefined);
});
