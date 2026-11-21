"use client";

import { useCallback, useEffect, useRef, type KeyboardEvent, type ReactNode } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";

const focusable = "a[href],button,input,select,textarea,[tabindex]";

export function VirtualList<T extends { id: string | number }>({ items, render, estimate, name, className = "" }: {
  items: T[];
  render: (item: T) => ReactNode;
  estimate: number;
  name: string;
  className?: string;
}) {
  "use no memo";

  const scroll = useRef<HTMLDivElement>(null);
  const pending = useRef<number | null>(null);
  const getItemKey = useCallback((index: number) => items[index].id, [items]);
  const list = useVirtualizer({
    count: items.length,
    getScrollElement: () => scroll.current,
    getItemKey,
    estimateSize: () => estimate,
    overscan: 4,
    useFlushSync: false,
  });

  useEffect(() => {
    if (pending.current !== null) focusRow(pending.current);
  });

  function focusRow(index: number) {
    const row = scroll.current?.querySelector<HTMLElement>(`[data-index="${index}"]`);
    const target = row?.querySelector<HTMLElement>(focusable);
    if (target) {
      pending.current = null;
      target.focus();
    } else {
      pending.current = row ? null : index;
    }
  }

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const target = event.target as HTMLElement;
    if (!items.length || target.closest("input,select,textarea")) return;
    const row = target.closest<HTMLElement>("[data-index]");
    const current = row && scroll.current?.contains(row) ? Number(row.dataset.index) : -1;
    const last = items.length - 1;
    const moves: Record<string, number> = { ArrowDown: current + 1, ArrowUp: current - 1, Home: 0, End: last };
    if (!(event.key in moves)) return;
    event.preventDefault();
    const index = Math.min(Math.max(moves[event.key], 0), last);
    list.scrollToIndex(index);
    focusRow(index);
  }

  return (
    <div ref={scroll} tabIndex={items.length ? 0 : undefined} role="region" aria-label={name} className={`virtual-scroll ${className}`}
      onKeyDown={onKeyDown}>
      <div className="virtual-range" role="list" style={{ height: list.getTotalSize() }}>
        {list.getVirtualItems().map((row) =>
          <div key={row.key} ref={list.measureElement} data-index={row.index} role="listitem"
            aria-posinset={row.index + 1} aria-setsize={items.length}
            className="virtual-row" style={{ transform: `translateY(${row.start}px)` }}>
            {render(items[row.index])}
          </div>)}
      </div>
    </div>
  );
}
