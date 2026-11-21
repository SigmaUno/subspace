"use client";

import { useEffect, useId, useRef, useState, type CSSProperties } from "react";
import { completeTour, tourSteps } from "../../../../lib/tour";
import { trapTab } from "../shared/ui";

type Box = { top: number; left: number; width: number; height: number };

const cardWidth = 352;
const edge = 12;

function anchor(target?: string) {
  return target ? document.querySelector<HTMLElement>(`[data-tour="${target}"]`) : null;
}

function locate(target?: string): Box | null {
  const element = anchor(target);
  if (!element || !element.getClientRects().length) return null;
  const { top, left, width, height } = element.getBoundingClientRect();
  return { top, left, width, height };
}

function placement(box: Box | null): CSSProperties | undefined {
  if (!box) return undefined;
  const left = Math.min(Math.max(box.left, edge), window.innerWidth - cardWidth - edge);
  if (box.top + box.height / 2 < window.innerHeight / 2) return { top: box.top + box.height + edge, left };
  return { bottom: window.innerHeight - box.top + edge, left };
}

export function ProductTour({ close }: { close: () => void }) {
  const titleID = useId();
  const bodyID = useId();
  const dialog = useRef<HTMLDivElement>(null);
  const [index, setIndex] = useState(0);
  const [box, setBox] = useState<Box | null>(null);
  const step = tourSteps[index];
  const last = tourSteps.length - 1;

  useEffect(() => {
    const returnFocus = document.activeElement;
    dialog.current?.focus();
    return () => {
      if (returnFocus instanceof HTMLElement) returnFocus.focus();
    };
  }, []);

  useEffect(() => {
    function refresh() {
      setBox(locate(step.target));
    }
    const frame = window.requestAnimationFrame(() => {
      anchor(step.target)?.scrollIntoView({ block: "center" });
      refresh();
    });
    window.addEventListener("resize", refresh);
    window.addEventListener("scroll", refresh, true);
    return () => {
      window.cancelAnimationFrame(frame);
      window.removeEventListener("resize", refresh);
      window.removeEventListener("scroll", refresh, true);
    };
  }, [step.target]);

  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape") close();
      else if (event.key === "ArrowRight") setIndex((current) => Math.min(current + 1, last));
      else if (event.key === "ArrowLeft") setIndex((current) => Math.max(current - 1, 0));
      else trapTab(event, dialog.current);
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [close, last]);

  function finish() {
    completeTour();
    close();
  }

  return <>
    <div className={box ? "tour-spot" : "tour-shade"} style={box ?? undefined} aria-hidden="true" />
    <div role="dialog" aria-modal="true" aria-labelledby={titleID} aria-describedby={bodyID} tabIndex={-1} ref={dialog}
      className={`panel stack tour-card ${box ? "" : "centered"}`} style={placement(box)}>
      <div className="heading">
        <span className="meta">Step {index + 1} of {tourSteps.length}</span>
        <button type="button" onClick={close}>Close</button>
      </div>
      <h2 id={titleID}>{step.title}</h2>
      <p id={bodyID}>{step.body}</p>
      <div className="actions">
        <button type="button" disabled={index === 0} onClick={() => setIndex(index - 1)}>Back</button>
        {index === last
          ? <button type="button" className="primary" onClick={finish}>Finish</button>
          : <button type="button" className="primary" onClick={() => setIndex(index + 1)}>Next</button>}
      </div>
    </div>
  </>;
}
