"use client";

import { useRouter } from "next/navigation";
import { Fragment, useEffect, useId, useRef, useState } from "react";
import { trapTab } from "./ui";

const destinations: Record<string, { href: string; action: string }> = {
  b: { href: "/briefs", action: "Go to briefs" },
  a: { href: "/agents", action: "Go to agents" },
  d: { href: "/", action: "Go to decisions" },
  c: { href: "/capacity", action: "Go to capacity" },
  o: { href: "/costs", action: "Go to costs" },
  u: { href: "/usage", action: "Go to usage" },
  t: { href: "/trends", action: "Go to trends" },
  p: { href: "/plans", action: "Go to plan approvals" },
  q: { href: "/questions", action: "Go to questions" },
  k: { href: "/knowledge", action: "Go to knowledge" },
  f: { href: "/files", action: "Go to files" },
  i: { href: "/instructions", action: "Go to instructions" },
  l: { href: "/logs", action: "Go to server logs" },
  s: { href: "/settings", action: "Go to settings, schedules and announcements" },
};

const shortcuts = [
  ...Object.entries(destinations).map(([key, { action }]) => ({ sequence: ["g", key], action })),
  { sequence: ["/"], action: "Focus the search field on this page" },
  { sequence: ["Ctrl+K"], action: "Open the command palette" },
  { sequence: ["?"], action: "Show or hide this list" },
  { sequence: ["Esc"], action: "Close dialogs, menus and panels" },
  { sequence: ["Shift+B"], action: "Board: toggle blocked tasks" },
  { sequence: ["Shift+S"], action: "Board: toggle stale-heartbeat tasks" },
  { sequence: ["Shift+M"], action: "Board: toggle merge-ready tasks" },
  { sequence: ["Shift+D"], action: "Board: toggle compact cards" },
  { sequence: ["Shift+X"], action: "Board: clear filters" },
  { sequence: ["A"], action: "Decision inbox (focus inside it): approve or answer the selected decision" },
  { sequence: ["R"], action: "Decision inbox (focus inside it): reject the selected decision" },
  { sequence: ["S"], action: "Decision inbox (focus inside it): skip to the next decision" },
  { sequence: ["T"], action: "Decision inbox (focus inside it): open the thread" },
  { sequence: ["Arrows, + / - / 0"], action: "Task graph: pan, zoom, reset the view" },
  { sequence: ["← / →"], action: "Trends: move the cursor between data points" },
];

export function typing(target: EventTarget | null) {
  return target instanceof HTMLElement && (target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName));
}

function focusSearch() {
  const field = document.querySelector<HTMLInputElement>('main input[type="search"]');
  if (field) field.focus();
  else window.dispatchEvent(new KeyboardEvent("keydown", { key: "k", ctrlKey: true }));
}

export function KeyboardShortcuts() {
  const router = useRouter();
  const titleID = useId();
  const dialog = useRef<HTMLDivElement>(null);
  const [help, setHelp] = useState(false);

  useEffect(() => {
    let chord = 0;
    function onKey(event: KeyboardEvent) {
      if (event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey || typing(event.target)) return;
      const awaiting = chord > Date.now();
      chord = 0;
      if (awaiting && destinations[event.key]) {
        event.preventDefault();
        router.push(destinations[event.key].href);
      } else if (event.key === "g") {
        chord = Date.now() + 1500;
      } else if (event.key === "?") {
        event.preventDefault();
        setHelp((open) => !open);
      } else if (event.key === "/") {
        event.preventDefault();
        focusSearch();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [router]);

  useEffect(() => {
    if (!help) return;
    const returnFocus = document.activeElement;
    dialog.current?.focus();
    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape") setHelp(false);
      else trapTab(event, dialog.current);
    }
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      if (returnFocus instanceof HTMLElement) returnFocus.focus();
    };
  }, [help]);

  if (!help) return null;

  return <div className="dialog-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget) setHelp(false); }}>
    <div role="dialog" aria-modal="true" aria-labelledby={titleID} tabIndex={-1} ref={dialog} className="panel stack shortcut-dialog">
      <div className="heading"><h2 id={titleID}>Keyboard shortcuts</h2><button type="button" onClick={() => setHelp(false)}>Close</button></div>
      <dl className="shortcut-list">
        {shortcuts.map((shortcut) => <div key={shortcut.action}>
          <dt>{shortcut.sequence.map((key, index) => <Fragment key={key}>{index > 0 && " then "}<kbd>{key}</kbd></Fragment>)}</dt>
          <dd>{shortcut.action}</dd>
        </div>)}
      </dl>
    </div>
  </div>;
}
