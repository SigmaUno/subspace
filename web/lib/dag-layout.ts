export interface DagInput {
  id: string;
  deps: readonly string[];
}

export interface LayoutOptions {
  nodeWidth: number;
  nodeHeight: number;
  layerGap: number;
  rowGap: number;
  sweeps: number;
}

export interface Point {
  x: number;
  y: number;
}

export interface LayoutNode {
  id: string;
  layer: number;
  row: number;
  x: number;
  y: number;
}

export interface LayoutEdge {
  from: string;
  to: string;
  points: Point[];
  reversed: boolean;
}

export interface Layout {
  nodes: LayoutNode[];
  edges: LayoutEdge[];
  width: number;
  height: number;
  layers: number;
  crossings: number;
}

export const defaultOptions: LayoutOptions = { nodeWidth: 200, nodeHeight: 56, layerGap: 80, rowGap: 16, sweeps: 12 };

function compareIds(a: string, b: string): number {
  const left = a.match(/\d+|\D+/g) ?? [];
  const right = b.match(/\d+|\D+/g) ?? [];
  const shared = Math.min(left.length, right.length);
  for (let i = 0; i < shared; i++) {
    const x = left[i];
    const y = right[i];
    if (x === y) continue;
    if (/^\d/.test(x) && /^\d/.test(y) && Number(x) !== Number(y)) return Number(x) - Number(y);
    return x < y ? -1 : 1;
  }
  return left.length - right.length;
}

function breakCycles(successors: number[][]): Set<number> {
  const count = successors.length;
  const state = new Uint8Array(count);
  const reversed = new Set<number>();
  for (let root = 0; root < count; root++) {
    if (state[root]) continue;
    state[root] = 1;
    const stack: [number, number][] = [[root, 0]];
    while (stack.length) {
      const top = stack[stack.length - 1];
      const [node, next] = top;
      if (next === successors[node].length) {
        state[node] = 2;
        stack.pop();
        continue;
      }
      top[1] += 1;
      const to = successors[node][next];
      if (state[to] === 1) {
        reversed.add(node * count + to);
      } else if (state[to] === 0) {
        state[to] = 1;
        stack.push([to, 0]);
      }
    }
  }
  return reversed;
}

function longestPathLayers(forward: number[][]): number[] {
  const indegree = new Array<number>(forward.length).fill(0);
  forward.forEach((targets) => targets.forEach((to) => { indegree[to] += 1; }));
  const layer = new Array<number>(forward.length).fill(0);
  const queue: number[] = [];
  indegree.forEach((degree, node) => { if (degree === 0) queue.push(node); });
  for (let head = 0; head < queue.length; head++) {
    const node = queue[head];
    for (const to of forward[node]) {
      layer[to] = Math.max(layer[to], layer[node] + 1);
      indegree[to] -= 1;
      if (indegree[to] === 0) queue.push(to);
    }
  }
  return layer;
}

export function layoutDag(input: readonly DagInput[], overrides: Partial<LayoutOptions> = {}): Layout {
  const options = { ...defaultOptions, ...overrides };
  const depsOf = new Map<string, readonly string[]>();
  for (const node of input) if (!depsOf.has(node.id)) depsOf.set(node.id, node.deps);
  const ids = [...depsOf.keys()].sort(compareIds);
  const count = ids.length;
  const index = new Map(ids.map((id, position) => [id, position]));

  const successors: number[][] = ids.map(() => []);
  ids.forEach((id, to) => {
    const sources = new Set<number>();
    for (const dep of depsOf.get(id) ?? []) {
      const from = index.get(dep);
      if (from !== undefined && from !== to) sources.add(from);
    }
    for (const from of sources) successors[from].push(to);
  });
  successors.forEach((targets) => targets.sort((a, b) => a - b));

  const reversed = breakCycles(successors);
  const forward = successors.map((targets, from) => targets.filter((to) => !reversed.has(from * count + to)));
  const layer = longestPathLayers(forward);

  const layerOf = [...layer];
  const up: number[][] = ids.map(() => []);
  const down: number[][] = ids.map(() => []);
  const routes: { from: number; to: number; path: number[] }[] = [];
  forward.forEach((targets, from) => {
    for (const to of targets) {
      const path = [from];
      for (let between = layer[from] + 1; between < layer[to]; between++) {
        path.push(layerOf.length);
        layerOf.push(between);
        up.push([]);
        down.push([]);
      }
      path.push(to);
      for (let step = 1; step < path.length; step++) {
        down[path[step - 1]].push(path[step]);
        up[path[step]].push(path[step - 1]);
      }
      routes.push({ from, to, path });
    }
  });

  const depth = layer.reduce((deepest, value) => Math.max(deepest, value + 1), 0);
  let order: number[][] = Array.from({ length: depth }, () => []);
  layerOf.forEach((l, node) => order[l].push(node));
  const position = new Array<number>(layerOf.length).fill(0);
  const place = () => order.forEach((row) => row.forEach((node, at) => { position[node] = at; }));
  place();

  const crossings = () => {
    let total = 0;
    for (let l = 0; l + 1 < depth; l++) {
      const pairs: [number, number][] = [];
      for (const node of order[l]) for (const to of down[node]) pairs.push([position[node], position[to]]);
      pairs.sort((a, b) => a[0] - b[0] || a[1] - b[1]);
      const tree = new Array<number>(order[l + 1].length + 1).fill(0);
      pairs.forEach(([, target], seen) => {
        let atOrBelow = 0;
        for (let i = target + 1; i > 0; i -= i & -i) atOrBelow += tree[i];
        total += seen - atOrBelow;
        for (let i = target + 1; i < tree.length; i += i & -i) tree[i] += 1;
      });
    }
    return total;
  };

  const reorder = (l: number, neighbours: number[][]) => {
    const keyed = order[l].map((node, at) => {
      const adjacent = neighbours[node];
      const key = adjacent.length ? adjacent.reduce((sum, other) => sum + position[other], 0) / adjacent.length : at;
      return { node, key, at };
    });
    keyed.sort((a, b) => a.key - b.key || a.at - b.at);
    order[l] = keyed.map((entry) => entry.node);
    order[l].forEach((node, at) => { position[node] = at; });
  };

  let bestCrossings = crossings();
  let best = order.map((row) => [...row]);
  for (let sweep = 0; sweep < options.sweeps && bestCrossings > 0; sweep++) {
    for (let l = 1; l < depth; l++) reorder(l, up);
    for (let l = depth - 2; l >= 0; l--) reorder(l, down);
    const total = crossings();
    if (total < bestCrossings) {
      bestCrossings = total;
      best = order.map((row) => [...row]);
    }
  }
  order = best;
  place();

  const strideX = options.nodeWidth + options.layerGap;
  const strideY = options.nodeHeight + options.rowGap;
  const middle = options.nodeHeight / 2;
  const nodes: LayoutNode[] = ids.map((id, node) => ({
    id, layer: layer[node], row: position[node], x: layer[node] * strideX, y: position[node] * strideY,
  }));

  const edges: LayoutEdge[] = routes.map(({ from, to, path }) => {
    const points: Point[] = [];
    path.forEach((node, step) => {
      const x = layerOf[node] * strideX;
      const y = position[node] * strideY + middle;
      if (step === 0) points.push({ x: x + options.nodeWidth, y });
      else if (step === path.length - 1) points.push({ x, y });
      else points.push({ x, y }, { x: x + options.nodeWidth, y });
    });
    return { from: ids[from], to: ids[to], points, reversed: false };
  });
  successors.forEach((targets, from) => {
    for (const to of targets) {
      if (!reversed.has(from * count + to)) continue;
      edges.push({
        from: ids[from], to: ids[to], reversed: true,
        points: [
          { x: nodes[from].x + options.nodeWidth, y: nodes[from].y + middle },
          { x: nodes[to].x, y: nodes[to].y + middle },
        ],
      });
    }
  });

  const rows = order.reduce((most, row) => Math.max(most, row.length), 0);
  return {
    nodes, edges,
    width: depth ? depth * strideX - options.layerGap : 0,
    height: rows ? rows * strideY - options.rowGap : 0,
    layers: depth,
    crossings: bestCrossings,
  };
}
