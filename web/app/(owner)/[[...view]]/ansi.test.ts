import assert from "node:assert/strict";
import { test } from "node:test";
import { color256, parseAnsi, plainText } from "./ansi.ts";

const esc = String.fromCharCode(27);

test("colours and resets become styled segments", () => {
  const segments = parseAnsi(`${esc}[1;31mFAIL${esc}[0m pkg ${esc}[38;5;46mok${esc}[39m${esc}[48;2;1;2;255m!`);
  assert.deepEqual(segments, [
    { text: "FAIL", style: { bold: true, fg: "#cd3131" } },
    { text: " pkg ", style: {} },
    { text: "ok", style: { fg: "#00ff00" } },
    { text: "!", style: { bg: "#0102ff" } },
  ]);
});

test("escape sequences and control characters never reach the text", () => {
  const hostile = `a${esc}]0;title${String.fromCharCode(7)}b${esc}[2Jc${esc}[?25ld${esc}e${String.fromCharCode(0x9b)}f\u0000g\th`;
  const text = plainText(hostile);
  assert.equal(text, "a]0;titleb[2Jc[?25ldefg\th");
  for (const char of text) {
    const code = char.codePointAt(0) ?? 0;
    assert.ok(code === 9 || (code >= 32 && (code < 127 || code > 159)), `control ${code} leaked`);
  }
});

test("invalid extended colours leave the style unchanged", () => {
  assert.deepEqual(parseAnsi(`${esc}[38;5;999mx`), [{ text: "x", style: {} }]);
  assert.deepEqual(parseAnsi(`${esc}[48;2;1;2mx`), [{ text: "x", style: {} }]);
  assert.deepEqual(parseAnsi(`${esc}[mx`), [{ text: "x", style: {} }]);
});

test("256 colour palette", () => {
  assert.equal(color256(1), "#cd3131");
  assert.equal(color256(9), "#f14c4c");
  assert.equal(color256(16), "#000000");
  assert.equal(color256(231), "#ffffff");
  assert.equal(color256(232), "#080808");
  assert.equal(color256(256), undefined);
});
