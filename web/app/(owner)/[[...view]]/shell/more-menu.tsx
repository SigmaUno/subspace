"use client";

import Link from "next/link";
import { useEffect, useId, useRef, useState } from "react";
import { moreNav } from "./nav";

export function MoreMenu({ section }: { section: string }) {
  const [openAt, setOpenAt] = useState<string | null>(null);
  const open = openAt === section;
  const root = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const menuID = useId();
  const active = moreNav.some((group) => group.items.some((item) => item.id === section));

  useEffect(() => {
    if (!open) return;
    function onKey(event: KeyboardEvent) {
      if (event.key !== "Escape") return;
      setOpenAt(null);
      button.current?.focus();
    }
    function onPointer(event: MouseEvent) {
      if (event.target instanceof Node && !root.current?.contains(event.target)) setOpenAt(null);
    }
    function onFocus(event: FocusEvent) {
      if (event.target instanceof Node && !root.current?.contains(event.target)) setOpenAt(null);
    }
    window.addEventListener("keydown", onKey);
    document.addEventListener("mousedown", onPointer);
    document.addEventListener("focusin", onFocus);
    return () => {
      window.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onPointer);
      document.removeEventListener("focusin", onFocus);
    };
  }, [open]);

  return <div className="nav-more" ref={root}>
    <button type="button" className="nav-more-button" ref={button} aria-expanded={open} aria-controls={menuID}
      aria-current={active ? "page" : undefined} onClick={() => setOpenAt(open ? null : section)}>More</button>
    {open && <div className="nav-menu" id={menuID}>
      {moreNav.map((group) => <div key={group.name} role="group" aria-label={group.name}>
        <span className="nav-group">{group.name}</span>
        {group.items.map((item) => <Link key={item.id} href={item.href} aria-current={section === item.id ? "page" : undefined}
          onClick={() => setOpenAt(null)}>{item.name}</Link>)}
      </div>)}
    </div>}
  </div>;
}
