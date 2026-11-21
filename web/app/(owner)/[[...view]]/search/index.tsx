"use client";

import { useRouter } from "next/navigation";
import { Fragment, useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { api } from "../../../../lib/client";
import { moreNav, primaryNav } from "../shell/nav";
import type { SearchKind, SearchResponse, SearchResult } from "../../../../lib/search-types";

type Item = { key: string; group: string; label: string; detail?: string; href: string };

const kinds: Record<SearchKind, { group: string; path: string } | null> = {
  brief: { group: "Briefs", path: "briefs" },
  task: { group: "Tasks", path: "tasks" },
  agent: { group: "Agents", path: "agents" },
  message: null,
};

const commands = [
  { id: "board", name: "Go to board", href: "/briefs" },
  { id: "decisions", name: "Go to decisions", href: "/" },
  ...[...primaryNav, ...moreNav.flatMap((group) => group.items)]
    .filter((item) => item.href !== "/" && item.id !== "briefs")
    .map((item) => ({ id: item.id, name: `Go to ${item.name.toLowerCase()}`, href: item.href })),
];

function resultItems(results: SearchResult[]): Item[] {
  const items: Item[] = [];
  for (const result of results) {
    const kind = kinds[result.kind];
    if (!kind) continue;
    items.push({
      key: `${result.kind}:${result.id}`,
      group: kind.group,
      label: result.id,
      detail: result.snippet,
      href: `/${kind.path}/${encodeURIComponent(result.id)}`,
    });
  }
  return items;
}

function Highlighted({ text }: { text: string }) {
  return <>{text.split(/[«»]/).map((part, index) => index % 2 ? <mark key={index}>{part}</mark> : part)}</>;
}

export function CommandPalette() {
  const router = useRouter();
  const listID = useId();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [term, setTerm] = useState("");
  const [active, setActive] = useState(0);
  const [found, setFound] = useState<{ term: string; results: SearchResult[]; error: string }>();
  const returnFocus = useRef<HTMLElement | null>(null);

  useEffect(() => {
    const timer = window.setTimeout(() => setTerm(query.trim()), 200);
    return () => window.clearTimeout(timer);
  }, [query]);

  useEffect(() => {
    if (!open || !term) return;
    const controller = new AbortController();
    api<SearchResponse>(`search?${new URLSearchParams({ q: term, limit: "20" })}`, "GET", undefined, controller.signal)
      .then((response) => setFound({ term, results: response.results, error: "" }))
      .catch((cause: Error) => {
        if (!controller.signal.aborted) setFound({ term, results: [], error: cause.message });
      });
    return () => controller.abort();
  }, [open, term]);

  useEffect(() => {
    if (!open) returnFocus.current?.focus();
  }, [open]);

  useEffect(() => {
    function onKey(event: globalThis.KeyboardEvent) {
      if (!(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== "k") return;
      event.preventDefault();
      if (!open) {
        returnFocus.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
        setQuery("");
        setActive(0);
      }
      setOpen(!open);
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);

  const needle = query.trim().toLowerCase();
  const results = found?.term === term ? found.results : [];
  const pending = !!term && found?.term !== term;
  const items: Item[] = [
    ...resultItems(results),
    ...commands
      .filter((command) => command.name.toLowerCase().includes(needle))
      .map((command) => ({ key: `command:${command.id}`, group: "Commands", label: command.name, href: command.href })),
  ];
  const current = Math.min(active, Math.max(items.length - 1, 0));
  const optionID = (index: number) => `${listID}-${index}`;

  useEffect(() => {
    document.getElementById(`${listID}-${current}`)?.scrollIntoView({ block: "nearest" });
  }, [listID, current]);

  function choose(item: Item) {
    setOpen(false);
    router.push(item.href);
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      setOpen(false);
    } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      if (!items.length) return;
      const step = event.key === "ArrowDown" ? 1 : -1;
      setActive((current + step + items.length) % items.length);
    } else if (event.key === "Enter" && items[current]) {
      event.preventDefault();
      choose(items[current]);
    }
  }

  if (!open) return null;

  return <div className="command-palette" onMouseDown={(event) => { if (event.target === event.currentTarget) setOpen(false); }}
    style={{ position: "fixed", inset: 0, zIndex: 50, display: "flex", justifyContent: "center", alignItems: "flex-start", paddingTop: "12vh", background: "rgba(15, 23, 42, .32)" }}>
    <div role="dialog" aria-modal="true" aria-label="Search and commands" className="panel stack"
      style={{ width: "min(40rem, 92vw)", boxShadow: "var(--shadow-lg)" }}>
      <input role="combobox" aria-label="Search Subspace or run a command" aria-expanded={items.length > 0}
        aria-controls={listID} aria-autocomplete="list" aria-activedescendant={items[current] ? optionID(current) : undefined}
        autoFocus placeholder="Search tasks, agents, briefs or type a command" value={query}
        onChange={(event) => { setQuery(event.target.value); setActive(0); }} onKeyDown={onKeyDown} />
      <ul id={listID} role="listbox" aria-label="Results" style={{ listStyle: "none", margin: 0, padding: 0, maxHeight: "50vh", overflowY: "auto" }}>
        {items.map((item, index) => <Fragment key={item.key}>
          {(index === 0 || items[index - 1].group !== item.group) && <li role="presentation" className="meta" style={{ padding: ".5rem .75rem 0" }}>{item.group}</li>}
          <li id={optionID(index)} role="option" aria-selected={index === current} onMouseEnter={() => setActive(index)}
            onClick={() => choose(item)}
            style={{ cursor: "pointer", padding: ".5rem .75rem", borderRadius: "var(--radius)", background: index === current ? "var(--accent-soft)" : undefined }}>
            <strong>{item.label}</strong>
            {item.detail && <div className="meta"><Highlighted text={item.detail} /></div>}
          </li>
        </Fragment>)}
      </ul>
      {pending && <p className="muted" role="status">Searching…</p>}
      {found?.term === term && found.error && <p className="error" role="alert">{found.error}</p>}
      {term && !pending && !found?.error && !items.some((item) => item.group !== "Commands") && <p className="empty" role="status">No matches for “{term}”.</p>}
    </div>
  </div>;
}
