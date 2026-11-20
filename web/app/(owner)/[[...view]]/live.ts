"use client";

import { useEffect, useState } from "react";

const eventScopes = {
  "task.created": ["board"],
  "task.updated": ["board"],
  "agent.updated": ["agents"],
  "spawn.updated": ["agents"],
  "message.created": ["thread"],
  "question.created": ["questions", "control"],
  "question.answered": ["questions", "control"],
  "plan.proposed": ["plans", "briefs", "board", "control"],
  "plan.approved": ["plans", "briefs", "board", "control"],
  "plan.rejected": ["plans", "briefs", "board", "control"],
  "control.paused": ["control", "board", "agents"],
  "control.resumed": ["control", "board", "agents"],
  "capacity.changed": ["capacity"],
} as const;

const scopes = ["board", "agents", "thread", "questions", "plans", "briefs", "control", "capacity"] as const;
type Scope = typeof scopes[number];

export function useLive(topics: string) {
  const [revisions, setRevisions] = useState<Record<Scope, number>>({
    board: 0, agents: 0, thread: 0, questions: 0, plans: 0, briefs: 0, control: 0, capacity: 0,
  });
  const [connected, setConnected] = useState(false);

  useEffect(() => {
    const stream = new EventSource(`/api/v1/events?${new URLSearchParams({ topics })}`);
    const pending = new Set<Scope>();
    let timer: ReturnType<typeof setTimeout> | undefined;

    function invalidate(changed: readonly Scope[]) {
      changed.forEach((scope) => pending.add(scope));
      if (timer) return;
      timer = setTimeout(() => {
        const refreshed = Array.from(pending);
        pending.clear();
        timer = undefined;
        setRevisions((current) => {
          const next = { ...current };
          refreshed.forEach((scope) => { next[scope] += 1; });
          return next;
        });
      }, 650);
    }

    stream.addEventListener("hello", () => { setConnected(true); invalidate(scopes); });
    stream.addEventListener("lagged", () => invalidate(scopes));
    stream.onerror = () => setConnected(false);
    Object.entries(eventScopes).forEach(([event, changed]) => {
      stream.addEventListener(event, () => invalidate(changed));
    });
    return () => {
      if (timer) clearTimeout(timer);
      stream.close();
    };
  }, [topics]);

  return { revisions, connected };
}
