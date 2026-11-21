"use client";

import { useRouter } from "next/navigation";
import { Fragment, useEffect, useId, useRef, useState } from "react";

const destinations: Record<string, string> = { b: "/briefs", a: "/agents", d: "/", c: "/capacity" };

const shortcuts = [
  { sequence: ["g", "b"], action: "Go to briefs" },
  { sequence: ["g", "a"], action: "Go to agents" },
  { sequence: ["g", "d"], action: "Go to decisions" },
  { sequence: ["g", "c"], action: "Go to capacity" },
  { sequence: ["/"], action: "Focus the search field on this page" },
  { sequence: ["Ctrl+K"], action: "Open the command palette" },
  { sequence: ["?"], action: "Show or hide this list" },
  { sequence: ["Esc"], action: "Close dialogs" },
  { sequence: ["Shift+B"], action: "Board: toggle blocked tasks" },
  { sequence: ["Shift+S"], action: "Board: toggle stale-heartbeat tasks" },
  { sequence: ["Shift+M"], action: "Board: toggle merge-ready tasks" },
  { sequence: ["Shift+D"], action: "Board: toggle compact cards" },
  { sequence: ["Shift+X"], action: "Board: clear filters" },
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
        router.push(destinations[event.key]);
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
    function onEscape(event: KeyboardEvent) {
      if (event.key === "Escape") setHelp(false);
    }
    window.addEventListener("keydown", onEscape);
    return () => {
      window.removeEventListener("keydown", onEscape);
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
