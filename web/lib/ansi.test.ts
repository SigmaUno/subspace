import assert from "node:assert/strict";
import { test } from "node:test";
import { ansiCss, applySgr, color256, parseAnsi, plainText } from "./ansi.ts";

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

function prng(seed: number) {
  let state = seed >>> 0;
  return () => {
    state = (state + 0x6d2b79f5) >>> 0;
    let t = state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function isInert(text: string) {
  for (const char of text) {
    const code = char.codePointAt(0) ?? 0;
    if (code === 27 || (code < 32 && code !== 9) || (code >= 127 && code <= 159)) return false;
  }
  return true;
}

test("terminal control strings are neutralised", () => {
  const payloads = [
    `${esc}]0;owned${String.fromCharCode(7)}`,
    `${esc}]8;;javascript:alert(1)${esc}\\click${esc}]8;;${esc}\\`,
    `${esc}Pq#0;2;0;0;0${esc}\\`,
    `${esc}[?1049h${esc}[2J${esc}[H`,
    `${esc}c${esc}[!p${esc}[6n`,
    `${esc}[31${esc}[1mtext`,
    `\u009d0;title\u009c\u009bm`,
  ];
  for (const payload of payloads) {
    const text = plainText(`before${payload}after`);
    assert.ok(isInert(text), JSON.stringify(text));
    assert.ok(text.startsWith("before") && text.endsWith("after"));
  }
});

test("only well formed SGR sequences are interpreted", () => {
  assert.deepEqual(parseAnsi(`${esc}[38:5:196mx`), [{ text: "[38:5:196mx", style: {} }]);
  assert.deepEqual(parseAnsi(`${esc}[31x${esc}[1my`), [{ text: "[31x", style: {} }, { text: "y", style: { bold: true } }]);
  assert.deepEqual(parseAnsi(`${esc}[31`), [{ text: "[31", style: {} }]);
  assert.deepEqual(parseAnsi(`${esc}[999;1;;4mx`), [{ text: "x", style: { underline: true } }]);
});

test("extended colours consume exactly their arguments", () => {
  assert.deepEqual(parseAnsi(`${esc}[1;38;2;1;2;3;4mx`), [{ text: "x", style: { bold: true, fg: "#010203", underline: true } }]);
  assert.deepEqual(parseAnsi(`${esc}[38;5mx`), [{ text: "x", style: {} }]);
  assert.deepEqual(parseAnsi(`${esc}[38;2;256;0;0;1mx`), [{ text: "x", style: {} }]);
  assert.deepEqual(parseAnsi(`${esc}[38;5;1;4mx`), [{ text: "x", style: { fg: "#cd3131", underline: true } }]);
  assert.deepEqual(parseAnsi(`${esc}[38;5;${"9".repeat(400)}mx`), [{ text: "x", style: {} }]);
});

test("attributes toggle off individually and reset clears all", () => {
  const input = `${esc}[1;2;3;4;7;9;31;42ma${esc}[22;23;24;27;29;39;49mb${esc}[1;31mc${esc}[0md`;
  assert.deepEqual(parseAnsi(input), [
    { text: "a", style: { bold: true, dim: true, italic: true, underline: true, inverse: true, strike: true, fg: "#cd3131", bg: "#0dbc79" } },
    { text: "b", style: { bold: false, dim: false, italic: false, underline: false, inverse: false, strike: false } },
    { text: "c", style: { bold: true, dim: false, italic: false, underline: false, inverse: false, strike: false, fg: "#cd3131" } },
    { text: "d", style: {} },
  ]);
});

test("applySgr does not mutate the previous style", () => {
  const before = { bold: true, fg: "#cd3131" };
  const after = applySgr(before, "0;4");
  assert.deepEqual(before, { bold: true, fg: "#cd3131" });
  assert.deepEqual(after, { underline: true });
});

test("unicode text survives and is never split by a sequence", () => {
  const text = "héllo 世界 😀👨‍👩‍👧 مرحبا";
  assert.equal(plainText(text), text);
  assert.deepEqual(parseAnsi(`${esc}[1m😀${esc}[0m😀`), [
    { text: "😀", style: { bold: true } },
    { text: "😀", style: {} },
  ]);
});

test("random byte soup never leaks control characters", () => {
  const alphabet = [esc, esc, "[", "[", "]", ";", ";", "0", "1", "3", "8", "5", "2", "m", "m", "\u0007", "\u009b", "\u009d", "\r", "\n", "\u0000", "a", " ", "😀", "\\"];
  const random = prng(20261121);
  for (let round = 0; round < 500; round++) {
    let line = "";
    for (let i = 0, length = Math.floor(random() * 80); i < length; i++) line += alphabet[Math.floor(random() * alphabet.length)];
    const segments = parseAnsi(line);
    for (const segment of segments) {
      assert.ok(segment.text.length > 0);
      assert.ok(isInert(segment.text), JSON.stringify(line));
    }
    assert.equal(plainText(line), segments.map((segment) => segment.text).join(""));
  }
});

test("huge hostile inputs are processed in linear time", { timeout: 10_000 }, () => {
  const unterminated = `${esc}[${"1;".repeat(500_000)}`;
  assert.equal(plainText(unterminated), `[${"1;".repeat(500_000)}`);
  assert.equal(plainText(`${esc}[`.repeat(300_000)), "[".repeat(300_000));
  const sequences = `${esc}[1m${esc}[0m`.repeat(200_000);
  assert.deepEqual(parseAnsi(`${sequences}end`), [{ text: "end", style: {} }]);
  const params = `${esc}[${"38;5;1;".repeat(100_000)}1mx`;
  assert.deepEqual(parseAnsi(params), [{ text: "x", style: { fg: "#cd3131", bold: true } }]);
  const wide = "x".repeat(2_000_000);
  assert.equal(plainText(wide).length, wide.length);
});

test("css maps inverse video and decorations", () => {
  assert.deepEqual(ansiCss({}), {
    color: undefined, backgroundColor: undefined, fontWeight: undefined, opacity: undefined, fontStyle: undefined, textDecoration: undefined,
  });
  assert.equal(ansiCss({ inverse: true }).color, "var(--terminal-bg)");
  assert.equal(ansiCss({ inverse: true }).backgroundColor, "var(--terminal-fg)");
  assert.deepEqual(
    [ansiCss({ inverse: true, fg: "#111111", bg: "#222222" }).color, ansiCss({ inverse: true, fg: "#111111", bg: "#222222" }).backgroundColor],
    ["#222222", "#111111"],
  );
  const styled = ansiCss({ bold: true, dim: true, italic: true, underline: true, strike: true });
  assert.equal(styled.textDecoration, "underline line-through");
  assert.equal(styled.fontWeight, 700);
  assert.equal(styled.opacity, 0.7);
  assert.equal(styled.fontStyle, "italic");
});
