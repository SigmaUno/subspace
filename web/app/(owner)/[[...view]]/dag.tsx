"use client";

import "./dag.css";
import Link from "next/link";
import { memo, useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { label, optionalResource, useResource } from "../../../lib/client";
import type { BriefReport } from "../../../lib/report-types";
import { statuses, type Board, type Task } from "../../../lib/types";
import { defaultOptions, layoutDag, type Layout, type Point } from "./dag-layout";
import { useLive } from "./live";
import { PageShell } from "./shell";
import { EmptyState, ResourceState } from "./states";

interface View {
  x: number;
  y: number;
  k: number;
}

interface Size {
  w: number;
  h: number;
}

const minZoom = 0.04;
const maxZoom = 3;
const dragThreshold = 4;
const ringRadius = 12;
const ringLength = 2 * Math.PI * ringRadius;

const clampZoom = (k: number) => Math.min(maxZoom, Math.max(minZoom, k));

function fitView(layout: Layout, size: Size): View {
  const pad = 24;
  const k = clampZoom(Math.min(1, (size.w - 2 * pad) / Math.max(layout.width, 1), (size.h - 2 * pad) / Math.max(layout.height, 1)));
  return { k, x: (size.w - layout.width * k) / 2, y: (size.h - layout.height * k) / 2 };
}

function zoomAt(view: View, px: number, py: number, factor: number): View {
  const k = clampZoom(view.k * factor);
  const ratio = k / view.k;
  return { k, x: px - (px - view.x) * ratio, y: py - (py - view.y) * ratio };
}

function curve(points: Point[]) {
  let d = `M ${points[0].x} ${points[0].y}`;
  for (let i = 1; i < points.length; i++) {
    const from = points[i - 1];
    const to = points[i];
    const half = (to.x - from.x) / 2;
    d += ` C ${from.x + half} ${from.y} ${to.x - half} ${to.y} ${to.x} ${to.y}`;
  }
  return d;
}

function detailAt(k: number) {
  if (k < 0.35) return 0;
  return k < 0.8 ? 1 : 2;
}

function shorten(text: string, limit: number) {
  return text.length > limit ? `${text.slice(0, limit - 1)}…` : text;
}

const Scene = memo(function Scene({ layout, tasks, criticalNodes, criticalEdges, detail, dragged }: {
  layout: Layout; tasks: Map<string, Task>; criticalNodes: Set<string>; criticalEdges: Set<string>;
  detail: number; dragged: () => boolean;
}) {
  const edges = layout.edges
    .map((edge) => ({ edge, hot: criticalEdges.has(`${edge.from}>${edge.to}`) || criticalEdges.has(`${edge.to}>${edge.from}`) }))
    .sort((a, b) => Number(a.hot) - Number(b.hot));
  const middle = defaultOptions.nodeHeight / 2;
  return <>
    {edges.map(({ edge, hot }) =>
      <path key={`${edge.from}>${edge.to}`} d={curve(edge.points)} markerEnd={`url(#${hot ? "dag-tip-hot" : "dag-tip"})`}
        className={`dag-edge${hot ? " hot" : ""}${edge.reversed ? " reversed" : ""}`} />)}
    {layout.nodes.map((node) => {
      const task = tasks.get(node.id);
      if (!task) return null;
      const progress = task.status === "done" ? 100 : Math.max(0, Math.min(100, task.progress_pct));
      const owner = task.assignee ?? "unassigned";
      return <Link key={node.id} href={`/tasks/${encodeURIComponent(node.id)}`} prefetch={false} draggable={false}
        aria-label={`${node.id} ${task.title}, ${label(task.status)}, ${progress}%, ${owner}`}
        onClick={(event) => { if (dragged()) event.preventDefault(); }}>
        <g className={`dag-node${criticalNodes.has(node.id) ? " hot" : ""}`} data-status={task.status}
          transform={`translate(${node.x} ${node.y})`}>
          <title>{node.id} {task.title} · {label(task.status)} · {progress}% · {owner}</title>
          <rect className="dag-box" width={defaultOptions.nodeWidth} height={defaultOptions.nodeHeight} rx={8} />
          {detail > 0 && <g transform={`translate(24 ${middle}) rotate(-90)`}>
            <circle className="dag-ring-track" r={ringRadius} />
            <circle className="dag-ring" r={ringRadius} strokeDasharray={ringLength} strokeDashoffset={ringLength * (1 - progress / 100)} />
          </g>}
          {detail === 1 && <text className="dag-id" x={46} y={middle} dominantBaseline="middle">{node.id}</text>}
          {detail === 2 && <>
            <text className="dag-id" x={46} y={19}>{node.id}</text>
            <text className="dag-title" x={46} y={34}>{shorten(task.title, 24)}</text>
            <text className="dag-meta" x={46} y={48}>{owner === "unassigned" ? owner : `@${shorten(owner, 22)}`}</text>
          </>}
        </g>
      </Link>;
    })}
  </>;
});

function Graph({ layout, tasks, criticalNodes, criticalEdges }: {
  layout: Layout; tasks: Map<string, Task>; criticalNodes: Set<string>; criticalEdges: Set<string>;
}) {
  const frame = useRef<SVGSVGElement>(null);
  const drag = useRef<{ x: number; y: number; from: View; moved: boolean } | null>(null);
  const dragged = useRef(false);
  const [size, setSize] = useState<Size>({ w: 960, h: 600 });
  const [manual, setManual] = useState<View | null>(null);
  const fitted = useMemo(() => fitView(layout, size), [layout, size]);
  const view = manual ?? fitted;
  const wasDragged = useCallback(() => dragged.current, []);

  useEffect(() => {
    const element = frame.current;
    if (!element) return;
    const observer = new ResizeObserver(([entry]) => {
      if (entry.contentRect.width > 0 && entry.contentRect.height > 0) setSize({ w: entry.contentRect.width, h: entry.contentRect.height });
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    const element = frame.current;
    if (!element) return;
    const onWheel = (event: WheelEvent) => {
      event.preventDefault();
      const box = element.getBoundingClientRect();
      setManual(zoomAt(view, event.clientX - box.left, event.clientY - box.top, Math.exp(-event.deltaY * 0.0015)));
    };
    element.addEventListener("wheel", onWheel, { passive: false });
    return () => element.removeEventListener("wheel", onWheel);
  }, [view]);

  function onPointerDown(event: PointerEvent<SVGSVGElement>) {
    if (event.button !== 0) return;
    dragged.current = false;
    drag.current = { x: event.clientX, y: event.clientY, from: view, moved: false };
  }

  function onPointerMove(event: PointerEvent<SVGSVGElement>) {
    const state = drag.current;
    if (!state) return;
    const dx = event.clientX - state.x;
    const dy = event.clientY - state.y;
    if (!state.moved) {
      if (Math.hypot(dx, dy) < dragThreshold) return;
      state.moved = true;
      dragged.current = true;
      event.currentTarget.setPointerCapture(event.pointerId);
    }
    setManual({ ...state.from, x: state.from.x + dx, y: state.from.y + dy });
  }

  function onPointerEnd(event: PointerEvent<SVGSVGElement>) {
    if (drag.current?.moved && event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
    drag.current = null;
  }

  function zoomBy(factor: number) {
    setManual(zoomAt(view, size.w / 2, size.h / 2, factor));
  }

  function onKeyDown(event: KeyboardEvent<SVGSVGElement>) {
    const step = 64;
    const moves: Record<string, [number, number]> = { ArrowLeft: [step, 0], ArrowRight: [-step, 0], ArrowUp: [0, step], ArrowDown: [0, -step] };
    const move = moves[event.key];
    if (move) setManual({ ...view, x: view.x + move[0], y: view.y + move[1] });
    else if (event.key === "+" || event.key === "=") zoomBy(1.25);
    else if (event.key === "-") zoomBy(0.8);
    else if (event.key === "0") setManual(null);
    else return;
    event.preventDefault();
  }

  return <div className="stack">
    <div className="actions dag-toolbar">
      <button type="button" onClick={() => zoomBy(1.25)} aria-label="Zoom in">+</button>
      <button type="button" onClick={() => zoomBy(0.8)} aria-label="Zoom out">−</button>
      <button type="button" onClick={() => setManual(null)}>Fit</button>
      <span className="meta" aria-live="polite">{Math.round(view.k * 100)}%</span>
      <span className="meta">Drag to pan, scroll to zoom, arrow keys and + / − from the graph</span>
    </div>
    <svg ref={frame} className="dag" role="group" tabIndex={0} aria-label="Task dependency graph"
      onKeyDown={onKeyDown} onPointerDown={onPointerDown} onPointerMove={onPointerMove}
      onPointerUp={onPointerEnd} onPointerCancel={onPointerEnd}>
      <defs>
        {["dag-tip", "dag-tip-hot"].map((id) => <marker key={id} id={id} viewBox="0 0 10 10" refX={9} refY={5}
          markerUnits="userSpaceOnUse" markerWidth={12} markerHeight={12} orient="auto"><path className={id} d="M 0 0 L 10 5 L 0 10 z" /></marker>)}
      </defs>
      <g transform={`translate(${view.x} ${view.y}) scale(${view.k})`}>
        <Scene layout={layout} tasks={tasks} criticalNodes={criticalNodes} criticalEdges={criticalEdges}
          detail={detailAt(view.k)} dragged={wasDragged} />
      </g>
    </svg>
  </div>;
}

export function DagPage({ id }: { id: string }) {
  const { revisions } = useLive(`brief:${id}`);
  const board = useResource<Board>(`briefs/${encodeURIComponent(id)}/board`, revisions.board);
  const report = useResource(`briefs/${encodeURIComponent(id)}/report`, 0, optionalResource<BriefReport>);
  const briefPath = `/briefs/${encodeURIComponent(id)}`;

  const tasks = useMemo(() => Object.values(board.data?.columns ?? {}).flatMap((column) => column ?? []).filter((task) => task.status !== "cancelled"), [board.data]);
  const byID = useMemo(() => new Map(tasks.map((task) => [task.id, task])), [tasks]);
  const structure = JSON.stringify(tasks.map((task) => [task.id, task.depends_on ?? []]));
  const layout = useMemo(
    () => layoutDag((JSON.parse(structure) as [string, string[]][]).map(([taskID, deps]) => ({ id: taskID, deps }))),
    [structure],
  );

  const path = useMemo(() => report.data?.critical_path.task_ids ?? [], [report.data]);
  const criticalNodes = useMemo(() => new Set(path), [path]);
  const criticalEdges = useMemo(() => new Set(path.slice(1).map((to, i) => `${path[i]}>${to}`)), [path]);
  const present = statuses.filter(([status]) => tasks.some((task) => task.status === status));

  return <PageShell section="briefs">
    <div className="heading">
      <div>
        <div className="meta"><Link href={briefPath}>{id}</Link> · graph</div>
        <h1>{board.data?.brief.title ?? "Task graph"}</h1>
      </div>
      <span className="meta">{tasks.length} tasks · {layout.layers} layers</span>
    </div>
    <ResourceState error={board.error} loading={!board.data} retry={board.refresh} rows={5} />
    {board.data && !tasks.length && <EmptyState>This brief has no tasks.</EmptyState>}
    {tasks.length > 0 && <section className="panel stack">
      <Graph layout={layout} tasks={byID} criticalNodes={criticalNodes} criticalEdges={criticalEdges} />
      <div className="actions">
        {present.map(([status, name]) => <span key={status} className="pill" data-status={status}>{name}</span>)}
        <span className="meta">{path.length ? `Critical path highlighted: ${path.join(" → ")}` : "No critical path data yet"}</span>
      </div>
    </section>}
  </PageShell>;
}
