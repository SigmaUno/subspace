import assert from "node:assert/strict";
import { test } from "node:test";
import { layoutDag, type DagInput, type Layout } from "./dag-layout.ts";

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

function shuffle<T>(items: T[], random: () => number) {
  const copy = [...items];
  for (let i = copy.length - 1; i > 0; i--) {
    const j = Math.floor(random() * (i + 1));
    [copy[i], copy[j]] = [copy[j], copy[i]];
  }
  return copy;
}

function layerOf(layout: Layout, id: string) {
  const node = layout.nodes.find((candidate) => candidate.id === id);
  assert.ok(node, `${id} is placed`);
  return node.layer;
}

function randomDag(count: number, seed: number): DagInput[] {
  const random = prng(seed);
  return Array.from({ length: count }, (_, i) => {
    const deps = new Set<string>();
    const wanted = i === 0 ? 0 : Math.floor(random() * 3);
    for (let k = 0; k < wanted; k++) deps.add(`T-${1 + Math.floor(random() * i)}`);
    return { id: `T-${i + 1}`, deps: [...deps] };
  });
}

test("longest path layering puts a task below its deepest dependency", () => {
  const layout = layoutDag([
    { id: "T-1", deps: [] },
    { id: "T-2", deps: ["T-1"] },
    { id: "T-3", deps: ["T-2"] },
    { id: "T-4", deps: ["T-1", "T-3"] },
    { id: "T-5", deps: [] },
  ]);
  assert.deepEqual(["T-1", "T-2", "T-3", "T-4", "T-5"].map((id) => layerOf(layout, id)), [0, 1, 2, 3, 0]);
  assert.equal(layout.layers, 4);
});

test("an edge spanning several layers is routed through one slot per skipped layer", () => {
  const layout = layoutDag([
    { id: "T-1", deps: [] },
    { id: "T-2", deps: ["T-1"] },
    { id: "T-3", deps: ["T-2"] },
    { id: "T-4", deps: ["T-1"] },
    { id: "T-5", deps: ["T-3", "T-4"] },
  ]);
  assert.equal(layerOf(layout, "T-5"), 3);
  const skip = layout.edges.find((edge) => edge.from === "T-4" && edge.to === "T-5");
  assert.equal(skip?.points.length, 4);
  const rows = new Set(layout.nodes.filter((node) => node.layer === 1).map((node) => node.row));
  assert.equal(rows.size, 2);
});

test("barycenter ordering removes crossings that the id order creates", () => {
  const layout = layoutDag([
    { id: "a1", deps: [] },
    { id: "a2", deps: [] },
    { id: "a3", deps: [] },
    { id: "b1", deps: ["a3"] },
    { id: "b2", deps: ["a2"] },
    { id: "b3", deps: ["a1"] },
  ]);
  assert.equal(layout.crossings, 0);
  const rowOf = (id: string) => layout.nodes.find((node) => node.id === id)!.row;
  assert.ok(rowOf("b3") < rowOf("b2") && rowOf("b2") < rowOf("b1"));
});

test("layout does not depend on input order", () => {
  const tasks = randomDag(120, 7);
  const expected = JSON.stringify(layoutDag(tasks));
  for (const seed of [1, 2, 3]) {
    const random = prng(seed);
    const permuted = shuffle(tasks, random).map((task) => ({ ...task, deps: shuffle([...task.deps], random) }));
    assert.equal(JSON.stringify(layoutDag(permuted)), expected);
  }
});

test("a dependency cycle is broken instead of failing", () => {
  const layout = layoutDag([
    { id: "T-1", deps: ["T-3"] },
    { id: "T-2", deps: ["T-1"] },
    { id: "T-3", deps: ["T-2"] },
    { id: "T-4", deps: ["T-4", "T-9"] },
  ]);
  assert.equal(layout.nodes.length, 4);
  assert.equal(layout.edges.length, 3);
  assert.equal(layout.edges.filter((edge) => edge.reversed).length, 1);
  assert.equal(layerOf(layout, "T-4"), 0);
});

test("500 tasks get unique cells and every dependency points forward", () => {
  const tasks = randomDag(500, 42);
  const layout = layoutDag(tasks);
  assert.equal(layout.nodes.length, 500);
  assert.equal(new Set(layout.nodes.map((node) => `${node.layer}:${node.row}`)).size, 500);
  for (const task of tasks) {
    for (const dep of task.deps) assert.ok(layerOf(layout, dep) < layerOf(layout, task.id));
  }
  assert.equal(layout.edges.length, tasks.reduce((sum, task) => sum + task.deps.length, 0));
});
