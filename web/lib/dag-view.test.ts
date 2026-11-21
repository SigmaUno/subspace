import assert from "node:assert/strict";
import { test } from "node:test";
import { clampZoom, criticalEdgeKeys, curve, detailAt, fitView, isHot, shorten, zoomAt, type View } from "./dag-view.ts";
import { layoutDag } from "./dag-layout.ts";

function prng(seed: number) {
  let state = seed >>> 0;
  return () => {
    state = (Math.imul(state, 1664525) + 1013904223) >>> 0;
    return state / 4294967296;
  };
}

test("zoom stays within bounds", () => {
  assert.equal(clampZoom(0), 0.04);
  assert.equal(clampZoom(100), 3);
  assert.equal(clampZoom(1.5), 1.5);
});

test("small graphs are centred at full size and large ones shrink to fit", () => {
  const small = { nodes: [], edges: [], width: 200, height: 100, layers: 1, crossings: 0 };
  assert.deepEqual(fitView(small, { w: 1000, h: 500 }), { k: 1, x: 400, y: 200 });
  const wide = { ...small, width: 10_000, height: 100 };
  const view = fitView(wide, { w: 1000, h: 500 });
  assert.equal(view.k, (1000 - 48) / 10_000);
  assert.ok(Math.abs(view.x + (wide.width * view.k) / 2 - 500) < 1e-9);
  const absurd = { ...small, width: 1e9, height: 1e9 };
  assert.equal(fitView(absurd, { w: 1000, h: 500 }).k, 0.04);
  assert.equal(fitView({ ...small, width: 0, height: 0 }, { w: 1000, h: 500 }).k, 1);
});

test("a real layout fits inside the viewport", () => {
  const ids = Array.from({ length: 200 }, (_, i) => `T-${i + 1}`);
  const layout = layoutDag(ids.map((id, i) => ({ id, deps: i ? [ids[Math.floor(i / 2)]] : [] })));
  const size = { w: 800, h: 600 };
  const view = fitView(layout, size);
  assert.ok(view.k <= 1 && view.k >= 0.04);
  if (view.k > 0.04) {
    assert.ok(layout.width * view.k <= size.w - 48 + 1e-6);
    assert.ok(layout.height * view.k <= size.h - 48 + 1e-6);
  }
});

test("zooming keeps the point under the cursor fixed", () => {
  const random = prng(7);
  for (let i = 0; i < 200; i++) {
    const view: View = { x: random() * 400 - 200, y: random() * 400 - 200, k: 0.05 + random() * 2.5 };
    const px = random() * 800;
    const py = random() * 600;
    const next = zoomAt(view, px, py, Math.exp((random() - 0.5) * 2));
    assert.ok(next.k >= 0.04 && next.k <= 3);
    assert.ok(Math.abs((px - view.x) / view.k - (px - next.x) / next.k) < 1e-6);
    assert.ok(Math.abs((py - view.y) / view.k - (py - next.y) / next.k) < 1e-6);
  }
});

test("zooming past the limit stops moving the view", () => {
  const view: View = { x: 10, y: 20, k: 3 };
  assert.deepEqual(zoomAt(view, 100, 100, 5), view);
});

test("edges are drawn as horizontal-tangent cubic segments", () => {
  assert.equal(curve([{ x: 0, y: 0 }, { x: 100, y: 40 }]), "M 0 0 C 50 0 50 40 100 40");
  assert.equal(
    curve([{ x: 0, y: 0 }, { x: 10, y: 10 }, { x: 10, y: 30 }]),
    "M 0 0 C 5 0 5 10 10 10 C 10 10 10 30 10 30",
  );
  assert.equal(curve([{ x: 3, y: 4 }]), "M 3 4");
});

test("detail levels switch at fixed zoom thresholds", () => {
  assert.deepEqual([0.04, 0.349, 0.35, 0.799, 0.8, 3].map(detailAt), [0, 0, 1, 1, 2, 2]);
});

test("labels are shortened with an ellipsis within the limit", () => {
  assert.equal(shorten("abcdef", 6), "abcdef");
  assert.equal(shorten("abcdefg", 6), "abcde…");
  assert.equal(shorten("abcdefg", 6).length, 6);
});

test("critical path edges are matched in both directions", () => {
  const edges = criticalEdgeKeys(["T-1", "T-3", "T-7"]);
  assert.deepEqual([...edges], ["T-1>T-3", "T-3>T-7"]);
  assert.ok(isHot(edges, "T-1", "T-3"));
  assert.ok(isHot(edges, "T-3", "T-1"));
  assert.ok(!isHot(edges, "T-1", "T-7"));
  assert.equal(criticalEdgeKeys([]).size, 0);
  assert.equal(criticalEdgeKeys(["T-1"]).size, 0);
});
