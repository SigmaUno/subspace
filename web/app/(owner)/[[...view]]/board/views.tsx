"use client";

import { useCallback, useMemo, useState, useSyncExternalStore, type FormEvent } from "react";
import { parseList, parseViews, removeView, saveView, toggleItem } from "../../../../lib/saved-views";
import type { Component } from "../../../../lib/swarm";
import { emptyQuery, formatQuery, none, parseQuery, statusGroups, type BoardQuery } from "../../../../lib/board-query";
import "./views.css";

const viewsKey = "subspace.board.views";
const densityKey = "subspace.board.density";
const collapsedKey = "subspace.board.collapsed";

type Density = "comfortable" | "compact";

const storageListeners = new Set<() => void>();

function subscribeStored(listener: () => void) {
  storageListeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    storageListeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

function readStored(key: string) {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStored(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
  }
  storageListeners.forEach((listener) => listener());
}

function useStored(key: string) {
  return useSyncExternalStore(subscribeStored, () => readStored(key), () => null);
}

export function useBoardPrefs() {
  const density: Density = useStored(densityKey) === "compact" ? "compact" : "comfortable";
  const collapsedRaw = useStored(collapsedKey);
  const collapsed = useMemo(() => new Set(parseList(collapsedRaw)), [collapsedRaw]);
  const setDensity = useCallback((next: Density) => writeStored(densityKey, next), []);
  const toggleCollapsed = useCallback((status: string) => {
    writeStored(collapsedKey, JSON.stringify(toggleItem(collapsed, status)));
  }, [collapsed]);
  return { density, setDensity, collapsed, toggleCollapsed };
}

function SavedViews({ query, onApply }: { query: BoardQuery; onApply: (query: BoardQuery) => void }) {
  const raw = useStored(viewsKey);
  const views = useMemo(() => parseViews(raw), [raw]);
  const [name, setName] = useState("");
  const [selected, setSelected] = useState("");

  function apply(value: string) {
    setSelected(value);
    const view = views.find((item) => item.name === value);
    if (view) onApply(parseQuery(view.query));
  }

  function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) return;
    writeStored(viewsKey, JSON.stringify(saveView(views, trimmed, formatQuery(query))));
    setSelected(trimmed);
    setName("");
  }

  function remove() {
    writeStored(viewsKey, JSON.stringify(removeView(views, selected)));
    setSelected("");
  }

  return <div className="saved-views">
    <select aria-label="Saved views" value={selected} onChange={(event) => apply(event.target.value)}>
      <option value="">Saved views</option>
      {views.map((view) => <option key={view.name} value={view.name}>{view.name}</option>)}
    </select>
    {views.some((view) => view.name === selected) && <button type="button" onClick={remove}>Delete view</button>}
    <form className="saved-view-form" onSubmit={save}>
      <input aria-label="View name" placeholder="Name this view" maxLength={60} value={name} onChange={(event) => setName(event.target.value)} />
      <button type="submit" disabled={!name.trim()}>Save view</button>
    </form>
  </div>;
}

export function BoardFilters({ query, onChange, facets, components, mergeSupported, shown, total, density, onDensity }: {
  query: BoardQuery;
  onChange: (query: BoardQuery) => void;
  facets: { assignees: string[]; roles: string[]; priorities: number[] };
  components: Component[];
  mergeSupported: boolean;
  shown: number;
  total: number;
  density: Density;
  onDensity: (density: Density) => void;
}) {
  const set = (patch: Partial<BoardQuery>) => onChange({ ...query, ...patch });
  const active = formatQuery(query) !== "";
  return <div className="board-filters stack">
    <div className="filter-row">
      <input className="filter-input" type="search" aria-label="Filter tasks" placeholder="Filter title, spec, id, assignee"
        value={query.text} onChange={(event) => set({ text: event.target.value })} />
      <select className="filter-input" aria-label="Assignee" value={query.assignee} onChange={(event) => set({ assignee: event.target.value })}>
        <option value="">Any assignee</option>
        <option value={none}>Unassigned</option>
        {facets.assignees.map((item) => <option key={item} value={item}>{item}</option>)}
      </select>
      <select className="filter-input" aria-label="Role" value={query.role} onChange={(event) => set({ role: event.target.value })}>
        <option value="">Any role</option>
        {facets.roles.map((item) => <option key={item} value={item}>{item}</option>)}
      </select>
      <select className="filter-input" aria-label="Status group" value={query.group} onChange={(event) => set({ group: event.target.value })}>
        <option value="">All statuses</option>
        {statusGroups.map((group) => <option key={group.id} value={group.id}>{group.name}</option>)}
      </select>
      <select className="filter-input" aria-label="Component" value={query.component} onChange={(event) => set({ component: event.target.value })}>
        <option value="">Any component</option>
        <option value={none}>No component</option>
        {components.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
      </select>
      <select className="filter-input" aria-label="Priority" value={query.priority} onChange={(event) => set({ priority: event.target.value })}>
        <option value="">Any priority</option>
        {facets.priorities.map((item) => <option key={item} value={String(item)}>Priority {item}</option>)}
      </select>
    </div>
    <div className="filter-row">
      <label className="inline" title="Shift+B"><input type="checkbox" checked={query.blocked} onChange={(event) => set({ blocked: event.target.checked })} /> Blocked or needs owner</label>
      <label className="inline" title="Shift+S"><input type="checkbox" checked={query.stale} onChange={(event) => set({ stale: event.target.checked })} /> Stale heartbeat</label>
      {mergeSupported && <label className="inline" title="Shift+M"><input type="checkbox" checked={query.mergeReady} onChange={(event) => set({ mergeReady: event.target.checked })} /> Merge ready</label>}
      <button type="button" aria-pressed={density === "compact"} title="Shift+D" onClick={() => onDensity(density === "compact" ? "comfortable" : "compact")}>Compact cards</button>
      <button type="button" disabled={!active} title="Shift+X" onClick={() => onChange(emptyQuery)}>Clear filters</button>
      <span className="meta" role="status">{shown} of {total} tasks</span>
    </div>
    <SavedViews query={query} onApply={onChange} />
  </div>;
}
