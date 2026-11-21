import type { Layout, Point } from "./dag-layout.ts";

export interface View {
  x: number;
  y: number;
  k: number;
}

export interface Size {
  w: number;
  h: number;
}

const minZoom = 0.04;
const maxZoom = 3;

export const clampZoom = (k: number) => Math.min(maxZoom, Math.max(minZoom, k));

export function fitView(layout: Layout, size: Size): View {
  const pad = 24;
  const k = clampZoom(Math.min(1, (size.w - 2 * pad) / Math.max(layout.width, 1), (size.h - 2 * pad) / Math.max(layout.height, 1)));
  return { k, x: (size.w - layout.width * k) / 2, y: (size.h - layout.height * k) / 2 };
}

export function zoomAt(view: View, px: number, py: number, factor: number): View {
  const k = clampZoom(view.k * factor);
  const ratio = k / view.k;
  return { k, x: px - (px - view.x) * ratio, y: py - (py - view.y) * ratio };
}

export function curve(points: Point[]) {
  let d = `M ${points[0].x} ${points[0].y}`;
  for (let i = 1; i < points.length; i++) {
    const from = points[i - 1];
    const to = points[i];
    const half = (to.x - from.x) / 2;
    d += ` C ${from.x + half} ${from.y} ${to.x - half} ${to.y} ${to.x} ${to.y}`;
  }
  return d;
}

export function detailAt(k: number) {
  if (k < 0.35) return 0;
  return k < 0.8 ? 1 : 2;
}

export function shorten(text: string, limit: number) {
  return text.length > limit ? `${text.slice(0, limit - 1)}…` : text;
}

export function criticalEdgeKeys(path: readonly string[]) {
  return new Set(path.slice(1).map((to, i) => `${path[i]}>${to}`));
}

export function isHot(edges: ReadonlySet<string>, from: string, to: string) {
  return edges.has(`${from}>${to}`) || edges.has(`${to}>${from}`);
}
