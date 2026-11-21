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

test("no tasks give an empty layout", () => {
  assert.deepEqual(layoutDag([]), { nodes: [], edges: [], width: 0, height: 0, layers: 0, crossings: 0 });
});

test("unknown, self and repeated dependencies are ignored", () => {
  const layout = layoutDag([
    { id: "T-1", deps: ["T-1", "T-404", "T-404"] },
    { id: "T-2", deps: ["T-1", "T-1", "T-1"] },
  ]);
  assert.equal(layout.edges.length, 1);
  assert.deepEqual([layerOf(layout, "T-1"), layerOf(layout, "T-2")], [0, 1]);
  assert.equal(layout.edges.some((edge) => edge.reversed), false);
});

test("a repeated id keeps its first definition", () => {
  const layout = layoutDag([
    { id: "T-1", deps: [] },
    { id: "T-2", deps: ["T-1"] },
    { id: "T-2", deps: [] },
  ]);
  assert.equal(layout.nodes.length, 2);
  assert.equal(layerOf(layout, "T-2"), 1);
});

test("ids with numbers are ordered naturally", () => {
  const layout = layoutDag(["T-10", "T-9", "T-100", "T-1"].map((id) => ({ id, deps: [] })));
  assert.deepEqual(layout.nodes.map((node) => node.id), ["T-1", "T-9", "T-10", "T-100"]);
  assert.deepEqual(layout.nodes.map((node) => node.row), [0, 1, 2, 3]);
});

test("random graphs with cycles never fail and never overlap", () => {
  const random = prng(99);
  for (let round = 0; round < 40; round++) {
    const count = 1 + Math.floor(random() * 30);
    const input: DagInput[] = Array.from({ length: count }, (_, i) => ({
      id: `T-${i + 1}`,
      deps: Array.from({ length: Math.floor(random() * 4) }, () => `T-${1 + Math.floor(random() * (count + 2))}`),
    }));
    const layout = layoutDag(input);
    assert.equal(layout.nodes.length, count);
    assert.equal(new Set(layout.nodes.map((node) => `${node.layer}:${node.row}`)).size, count);
    for (const edge of layout.edges) {
      assert.ok(edge.points.length >= 2);
      assert.ok(edge.points.every((point) => Number.isFinite(point.x) && Number.isFinite(point.y)));
      if (!edge.reversed) assert.ok(layerOf(layout, edge.from) < layerOf(layout, edge.to));
    }
    assert.deepEqual(layoutDag(input), layout);
  }
});

test("a long chain and a wide fan stay linear in size", { timeout: 20_000 }, () => {
  const chain = Array.from({ length: 2000 }, (_, i) => ({ id: `T-${i + 1}`, deps: i ? [`T-${i}`] : [] }));
  const chained = layoutDag(chain);
  assert.equal(chained.layers, 2000);
  assert.equal(chained.crossings, 0);
  const fan = [{ id: "T-0", deps: [] as string[] }, ...Array.from({ length: 2000 }, (_, i) => ({ id: `T-${i + 1}`, deps: ["T-0"] }))];
  const fanned = layoutDag(fan);
  assert.equal(fanned.layers, 2);
  assert.equal(fanned.edges.length, 2000);
});
