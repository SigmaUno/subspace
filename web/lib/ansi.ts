import type { CSSProperties } from "react";

interface AnsiStyle {
  fg?: string;
  bg?: string;
  bold?: boolean;
  dim?: boolean;
  italic?: boolean;
  underline?: boolean;
  strike?: boolean;
  inverse?: boolean;
}

interface AnsiSegment {
  text: string;
  style: AnsiStyle;
}

const base = ["#1e1e1e", "#cd3131", "#0dbc79", "#e5e510", "#2472c8", "#bc3fbc", "#11a8cd", "#e5e5e5"];
const bright = ["#666666", "#f14c4c", "#23d18b", "#f5f543", "#3b8eea", "#d670d6", "#29b8db", "#ffffff"];
const cube = [0, 95, 135, 175, 215, 255];
const escape = 27;

const hex = (r: number, g: number, b: number) =>
  `#${[r, g, b].map((part) => part.toString(16).padStart(2, "0")).join("")}`;

export function color256(n: number): string | undefined {
  if (!Number.isInteger(n) || n < 0 || n > 255) return undefined;
  if (n < 8) return base[n];
  if (n < 16) return bright[n - 8];
  if (n < 232) {
    const index = n - 16;
    return hex(cube[Math.floor(index / 36)], cube[Math.floor(index / 6) % 6], cube[index % 6]);
  }
  const gray = 8 + (n - 232) * 10;
  return hex(gray, gray, gray);
}

function extended(codes: number[], at: number): [string | undefined, number] {
  if (codes[at + 1] === 5) return [color256(codes[at + 2]), 2];
  if (codes[at + 1] === 2) {
    const rgb = codes.slice(at + 2, at + 5);
    if (rgb.length === 3 && rgb.every((part) => Number.isInteger(part) && part >= 0 && part <= 255)) return [hex(rgb[0], rgb[1], rgb[2]), 4];
  }
  return [undefined, codes.length];
}

export function applySgr(current: AnsiStyle, params: string): AnsiStyle {
  let style: AnsiStyle = { ...current };
  const codes = params === "" ? [0] : params.split(";").map((part) => part === "" ? 0 : Number(part));
  for (let i = 0; i < codes.length; i++) {
    const code = codes[i];
    if (code === 0) style = {};
    else if (code === 1) style.bold = true;
    else if (code === 2) style.dim = true;
    else if (code === 3) style.italic = true;
    else if (code === 4) style.underline = true;
    else if (code === 7) style.inverse = true;
    else if (code === 9) style.strike = true;
    else if (code === 22) { style.bold = false; style.dim = false; }
    else if (code === 23) style.italic = false;
    else if (code === 24) style.underline = false;
    else if (code === 27) style.inverse = false;
    else if (code === 29) style.strike = false;
    else if (code >= 30 && code <= 37) style.fg = base[code - 30];
    else if (code === 39) delete style.fg;
    else if (code >= 40 && code <= 47) style.bg = base[code - 40];
    else if (code === 49) delete style.bg;
    else if (code >= 90 && code <= 97) style.fg = bright[code - 90];
    else if (code >= 100 && code <= 107) style.bg = bright[code - 100];
    else if (code === 38 || code === 48) {
      const [color, used] = extended(codes, i);
      if (color) style[code === 38 ? "fg" : "bg"] = color;
      i += used;
    }
  }
  return style;
}

function printable(text: string): string {
  let out = "";
  for (const char of text) {
    const code = char.codePointAt(0) ?? 0;
    if ((code < 32 && code !== 9) || (code >= 127 && code <= 159)) continue;
    out += char;
  }
  return out;
}

const isParam = (char: string | undefined) => char !== undefined && (char === ";" || (char >= "0" && char <= "9"));

export function parseAnsi(line: string): AnsiSegment[] {
  const segments: AnsiSegment[] = [];
  let style: AnsiStyle = {};
  let start = 0;
  const push = (end: number) => {
    const text = printable(line.slice(start, end));
    if (text) segments.push({ text, style });
  };
  for (let i = 0; i < line.length; i++) {
    if (line.charCodeAt(i) !== escape || line[i + 1] !== "[") continue;
    let end = i + 2;
    while (isParam(line[end])) end++;
    if (line[end] !== "m") continue;
    push(i);
    style = applySgr(style, line.slice(i + 2, end));
    start = end + 1;
    i = end;
  }
  push(line.length);
  return segments;
}

export function plainText(line: string): string {
  return parseAnsi(line).map((segment) => segment.text).join("");
}

export function ansiCss(style: AnsiStyle): CSSProperties {
  const fg = style.inverse ? style.bg ?? "var(--terminal-bg)" : style.fg;
  const bg = style.inverse ? style.fg ?? "var(--terminal-fg)" : style.bg;
  const decoration = [style.underline && "underline", style.strike && "line-through"].filter(Boolean).join(" ");
  return {
    color: fg,
    backgroundColor: bg,
    fontWeight: style.bold ? 700 : undefined,
    opacity: style.dim ? 0.7 : undefined,
    fontStyle: style.italic ? "italic" : undefined,
    textDecoration: decoration || undefined,
  };
}
