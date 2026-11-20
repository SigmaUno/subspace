"use client";

import Link from "next/link";
import { useState, type KeyboardEvent } from "react";
import { api, commaList, label, useResource, useSubmit } from "../../../lib/client";
import type {
  ArchitectureEdge, ArchitectureGraph, ArchitectureNode, ArchitectureRollup, ComponentSpec, RollupState,
} from "../../../lib/architecture-types";
import type { Board, Task } from "../../../lib/types";
import { Feedback } from "./components";
import { useLive } from "./live";

const NODE_W = 220;
const NODE_H = 98;
const GAP_X = 96;
const GAP_Y = 28;
const PAD = 20;

const states: [RollupState, string][] = [
  ["empty", "No tasks"], ["pending", "Waiting"], ["active", "In progress"],
  ["stale", "Stale"], ["blocked", "Blocked"], ["done", "Done"],
];
const stateLabel = Object.fromEntries(states) as Record<RollupState, string>;

const nav = [
  ["briefs", "Briefs"], ["agents", "Agents"], ["plans", "Plans"], ["questions", "Questions"], ["capacity", "Capacity"],
];

function rollupState(rollup: ArchitectureRollup): RollupState {
  const counts = rollup.counts ?? {};
  const live = rollup.total - (counts.cancelled ?? 0);
  if (live <= 0) return "empty";
  if (rollup.blocked > 0) return "blocked";
  if (rollup.stale > 0) return "stale";
  if ((counts.done ?? 0) === live) return "done";
  if ((counts.backlog ?? 0) + (counts.ready ?? 0) === live) return "pending";
  return "active";
}

function clip(text: string, max: number) {
  return text.length > max ? `${text.slice(0, max - 1)}…` : text;
}

function layout(nodes: ArchitectureNode[]) {
  const known = new Set(nodes.map((node) => node.id));
  const byID = new Map(nodes.map((node) => [node.id, node]));
  const depth = new Map<string, number>();

  function visit(id: string, path: Set<string>): number {
    const cached = depth.get(id);
    if (cached !== undefined) return cached;
    if (path.has(id)) return 0;
    path.add(id);
    let result = 0;
    for (const dependency of byID.get(id)?.depends_on ?? []) {
      if (known.has(dependency)) result = Math.max(result, visit(dependency, path) + 1);
    }
    path.delete(id);
    depth.set(id, result);
    return result;
  }

  const columns: ArchitectureNode[][] = [];
  for (const node of nodes) {
    const column = visit(node.id, new Set());
    columns[column] = [...(columns[column] ?? []), node];
  }

  const positions = new Map<string, { x: number; y: number }>();
  columns.forEach((column, c) => column.forEach((node, r) => {
    positions.set(node.id, { x: PAD + c * (NODE_W + GAP_X), y: PAD + r * (NODE_H + GAP_Y) });
  }));

  const rows = Math.max(0, ...columns.map((column) => column.length));
  return {
    positions,
    width: PAD * 2 + columns.length * NODE_W + Math.max(0, columns.length - 1) * GAP_X,
    height: PAD * 2 + rows * NODE_H + Math.max(0, rows - 1) * GAP_Y,
  };
}

function GraphNode({ node, x, y, selected, select }: {
  node: ArchitectureNode; x: number; y: number; selected: boolean; select: () => void;
}) {
  const state = rollupState(node.status);
  const owners = node.owners ?? [];
  const progress = Math.max(0, Math.min(100, node.status.progress_avg));
  const ownerText = owners.length
    ? `${owners.slice(0, 2).map((owner) => owner.agent_id).join(", ")}${owners.length > 2 ? ` +${owners.length - 2}` : ""}`
    : "No owners";

  function onKeyDown(event: KeyboardEvent<SVGGElement>) {
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      select();
    }
  }

  return (
    <g
      className={`arch-node ${state}${selected ? " selected" : ""}`}
      transform={`translate(${x} ${y})`}
      role="button"
      tabIndex={0}
      aria-pressed={selected}
      aria-label={`${node.name}, ${stateLabel[state]}, ${progress}% complete`}
      onClick={select}
      onKeyDown={onKeyDown}
    >
      <title>{node.name}</title>
      <rect className="arch-card" width={NODE_W} height={NODE_H} rx={8} />
      <text className="arch-name" x={12} y={26}>{clip(node.name, 24)}</text>
      <text className="arch-meta" x={12} y={46}>{stateLabel[state]} · {node.status.total} {node.status.total === 1 ? "task" : "tasks"}</text>
      <text className="arch-meta" x={NODE_W - 12} y={46} textAnchor="end">{progress}%</text>
      <rect className="arch-track" x={12} y={56} width={NODE_W - 24} height={6} rx={3} />
      <rect className="arch-fill" x={12} y={56} width={((NODE_W - 24) * progress) / 100} height={6} rx={3} />
      <text className="arch-meta" x={12} y={84}>{clip(ownerText, 30)}</text>
    </g>
  );
}

function edgePath(edge: ArchitectureEdge, positions: Map<string, { x: number; y: number }>) {
  const dependency = positions.get(edge.to);
  const dependent = positions.get(edge.from);
  if (!dependency || !dependent || dependency.x >= dependent.x) return null;
  const x1 = dependency.x + NODE_W;
  const y1 = dependency.y + NODE_H / 2;
  const x2 = dependent.x;
  const y2 = dependent.y + NODE_H / 2;
  const bend = (x2 - x1) / 2;
  return `M${x1} ${y1} C${x1 + bend} ${y1} ${x2 - bend} ${y2} ${x2} ${y2}`;
}

function NodePanel({ node, nodes, tasks, close }: {
  node: ArchitectureNode; nodes: ArchitectureNode[]; tasks: Map<string, Task>; close: () => void;
}) {
  const state = rollupState(node.status);
  const names = new Map(nodes.map((entry) => [entry.id, entry.name]));
  const dependsOn = node.depends_on ?? [];
  const requiredBy = nodes.filter((entry) => entry.depends_on?.includes(node.id));
  const counts = Object.entries(node.status.counts ?? {}).filter(([, count]) => count > 0);

  return (
    <aside className="panel stack arch-panel" aria-label={`${node.name} details`}>
      <div className="heading">
        <div>
          <div className="meta">{node.id}</div>
          <h2>{node.name}</h2>
        </div>
        <button onClick={close}>Close</button>
      </div>
      <div className="arch-legend-item"><span className={`arch-swatch ${state}`} aria-hidden="true" />{stateLabel[state]} · {node.status.progress_avg}% average progress</div>
      {node.description && <p className="preserve">{node.description}</p>}
      {!!node.paths?.length && <div><h3>Paths</h3><ul className="arch-list">{node.paths.map((path) => <li key={path}><code>{path}</code></li>)}</ul></div>}
      {!!dependsOn.length && <div><h3>Depends on</h3><p>{dependsOn.map((id) => names.get(id) ?? id).join(", ")}</p></div>}
      {!!requiredBy.length && <div><h3>Required by</h3><p>{requiredBy.map((entry) => entry.name).join(", ")}</p></div>}
      <div>
        <h3>Tasks</h3>
        {counts.length
          ? <p className="meta">{counts.map(([status, count]) => `${count} ${label(status)}`).join(" · ")}</p>
          : <p className="meta">No tasks linked to this component.</p>}
        {node.status.blocked > 0 && <p className="stale">{node.status.blocked} blocked</p>}
        {node.status.stale > 0 && <p className="stale">{node.status.stale} stale</p>}
        {node.status.last_activity && <p className="meta">Last activity {new Date(node.status.last_activity).toLocaleString()}</p>}
      </div>
      <div>
        <h3>Owners</h3>
        {!node.owners?.length && <p className="meta">No owners assigned.</p>}
        <ul className="arch-list">
          {node.owners?.map((owner) => (
            <li key={owner.agent_id}>
              <div><strong>{owner.agent_id}</strong> <span className="meta">{owner.role}</span> <span className={`pill ${owner.agent_status}`}>{owner.agent_status}</span></div>
              <ul className="arch-list">
                {owner.task_ids?.map((id) => {
                  const task = tasks.get(id);
                  return (
                    <li key={id} className="arch-task">
                      <span className="break">{id}{task && ` · ${task.title}`}</span>
                      {task && (
                        <>
                          <span className="meta">{label(task.status)}</span>
                          <span className="progress"><progress aria-label={`${id} progress`} value={task.progress_pct} max={100} /><span>{task.progress_pct}%</span></span>
                        </>
                      )}
                    </li>
                  );
                })}
              </ul>
            </li>
          ))}
        </ul>
      </div>
    </aside>
  );
}

interface Row {
  key: number;
  name: string;
  description: string;
  paths: string;
  deps: string;
}

function rowsFrom(nodes: ArchitectureNode[]): Row[] {
  const names = new Map(nodes.map((node) => [node.id, node.name]));
  const rows = nodes.map((node, key) => ({
    key,
    name: node.name,
    description: node.description,
    paths: (node.paths ?? []).join(", "),
    deps: (node.depends_on ?? []).map((id) => names.get(id) ?? id).join(", "),
  }));
  return rows.length ? rows : [{ key: 0, name: "", description: "", paths: "", deps: "" }];
}

function ComponentForm({ briefID, initial, saved, cancel }: {
  briefID: string; initial: Row[]; saved: () => void; cancel: () => void;
}) {
  const [rows, setRows] = useState(initial);
  const form = useSubmit();

  function update(key: number, patch: Partial<Row>) {
    setRows(rows.map((row) => (row.key === key ? { ...row, ...patch } : row)));
  }

  function add() {
    const key = Math.max(...rows.map((row) => row.key), -1) + 1;
    setRows([...rows, { key, name: "", description: "", paths: "", deps: "" }]);
  }

  return (
    <form className="panel stack" onSubmit={(event) => form.submit(event, async () => {
      const components: ComponentSpec[] = rows.map((row) => ({
        name: row.name.trim(),
        description: row.description.trim(),
        paths: commaList(row.paths),
        depends_on: commaList(row.deps),
      }));
      await api(`briefs/${encodeURIComponent(briefID)}/components`, "PUT", { components });
      saved();
    })}>
      <h2>Define components</h2>
      <p className="meta">Saving replaces the current component plan. Dependencies are component names.</p>
      {rows.map((row, index) => (
        <fieldset className="stack" key={row.key}>
          <legend className="meta">Component {index + 1}</legend>
          <div className="form-grid">
            <label>Name<input value={row.name} onChange={(event) => update(row.key, { name: event.target.value })} required /></label>
            <label>Depends on<input value={row.deps} onChange={(event) => update(row.key, { deps: event.target.value })} placeholder="core, storage" /></label>
            <label>Paths<input value={row.paths} onChange={(event) => update(row.key, { paths: event.target.value })} placeholder="internal/core, cmd/server" /></label>
          </div>
          <label>Description<textarea rows={2} value={row.description} onChange={(event) => update(row.key, { description: event.target.value })} /></label>
          {rows.length > 1 && <div><button type="button" onClick={() => setRows(rows.filter((entry) => entry.key !== row.key))}>Remove component {index + 1}</button></div>}
        </fieldset>
      ))}
      <Feedback error={form.error} />
      <div className="actions">
        <button type="button" onClick={add}>Add component</button>
        <button type="submit" className="primary" disabled={form.busy}>Save components</button>
        <button type="button" onClick={cancel}>Cancel</button>
      </div>
    </form>
  );
}

export function Architecture({ briefID }: { briefID: string }) {
  const id = encodeURIComponent(briefID);
  const { revisions, connected } = useLive(`agents,brief:${briefID}`);
  const graph = useResource<ArchitectureGraph>(`briefs/${id}/architecture`, revisions.board + revisions.agents);
  const board = useResource<Board>(`briefs/${id}/board`, revisions.board);
  const [selected, setSelected] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);

  const nodes = graph.data?.nodes ?? [];
  const edges = graph.data?.edges ?? [];
  const { positions, width, height } = layout(nodes);
  const node = nodes.find((entry) => entry.id === selected);
  const tasks = new Map<string, Task>();
  for (const column of Object.values(board.data?.columns ?? {})) {
    for (const task of column ?? []) tasks.set(task.id, task);
  }

  return (
    <>
      <header className="topbar">
        <Link className="brand" href="/briefs"><span className="brand-mark" aria-hidden="true" />Subspace</Link>
        <nav aria-label="Workspace">
          {nav.map(([section, name]) => <Link key={section} href={`/${section}`}>{name}</Link>)}
        </nav>
        <div className="top-actions">
          <span className={`connection ${connected ? "online" : ""}`} role="status">{connected ? "Live" : "Reconnecting"}</span>
        </div>
      </header>
      <main className="workspace wide">
        <Link href={`/briefs/${id}`} className="back">Back to board</Link>
        <div className="heading">
          <div>
            <div className="meta">{briefID} · architecture</div>
            <h1>{board.data?.brief.title ?? "Architecture"}</h1>
          </div>
          <button className="primary" onClick={() => setEditing(!editing)}>{editing ? "Close form" : "Define components"}</button>
        </div>
        <Feedback error={graph.error || board.error} loading={!graph.data} />
        {editing && (
          <ComponentForm
            key={graph.data ? "loaded" : "loading"}
            briefID={briefID}
            initial={rowsFrom(nodes)}
            saved={() => { setEditing(false); setSelected(null); graph.refresh(); }}
            cancel={() => setEditing(false)}
          />
        )}
        {graph.data && !nodes.length && <p className="empty">No components defined yet. Use “Define components” to describe the architecture.</p>}
        {!!nodes.length && (
          <>
            <ul className="arch-legend" aria-label="Status colours">
              {states.map(([state, name]) => <li key={state} className="arch-legend-item"><span className={`arch-swatch ${state}`} aria-hidden="true" />{name}</li>)}
            </ul>
            <div className={`arch-layout ${node ? "with-panel" : ""}`}>
              <div className="arch-canvas panel" tabIndex={0} aria-label="Component graph">
                <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="group" aria-label="Component dependency graph">
                  <defs>
                    <marker id="arch-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto">
                      <path className="arch-arrow" d="M0 0L10 5L0 10z" />
                    </marker>
                  </defs>
                  {edges.map((edge) => {
                    const path = edgePath(edge, positions);
                    if (!path) return null;
                    const linked = selected === edge.from || selected === edge.to;
                    return <path key={`${edge.from}-${edge.to}`} className={`arch-edge${linked ? " linked" : ""}`} d={path} markerEnd="url(#arch-arrow)" />;
                  })}
                  {nodes.map((entry) => {
                    const position = positions.get(entry.id);
                    if (!position) return null;
                    return (
                      <GraphNode
                        key={entry.id}
                        node={entry}
                        x={position.x}
                        y={position.y}
                        selected={entry.id === selected}
                        select={() => setSelected(entry.id === selected ? null : entry.id)}
                      />
                    );
                  })}
                </svg>
              </div>
              {node && <NodePanel node={node} nodes={nodes} tasks={tasks} close={() => setSelected(null)} />}
            </div>
          </>
        )}
      </main>
    </>
  );
}
