"use client";

import { useCallback, useRef, type ReactNode } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";

export function VirtualList<T extends { id: string | number }>({ items, render, estimate, name, className = "" }: {
  items: T[];
  render: (item: T) => ReactNode;
  estimate: number;
  name: string;
  className?: string;
}) {
  "use no memo";

  const scroll = useRef<HTMLDivElement>(null);
  const getItemKey = useCallback((index: number) => items[index].id, [items]);
  const list = useVirtualizer({
    count: items.length,
    getScrollElement: () => scroll.current,
    getItemKey,
    estimateSize: () => estimate,
    overscan: 4,
    useFlushSync: false,
  });

  return (
    <div ref={scroll} tabIndex={items.length ? 0 : undefined} role="region" aria-label={name} className={`virtual-scroll ${className}`}>
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
