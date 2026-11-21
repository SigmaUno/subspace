"use client";

import { useEffect, useState } from "react";
import type { Message } from "../../../lib/types";

const scopes = [
  "board", "agents", "thread", "questions", "plans", "briefs", "control", "capacity",
  "decisions", "architecture", "chat", "metrics",
] as const;
type Scope = typeof scopes[number];
export type Revisions = Record<Scope, number>;

const eventScopes = {
  "task.created": ["board", "decisions"],
  "agent.updated": ["agents", "capacity"],
  "spawn.updated": ["agents", "capacity"],
  "question.created": ["questions", "control", "decisions"],
  "question.answered": ["questions", "control", "decisions"],
  "plan.proposed": ["plans", "briefs", "board", "control", "decisions", "architecture"],
  "plan.approved": ["plans", "briefs", "board", "control", "decisions", "architecture"],
  "plan.rejected": ["plans", "briefs", "board", "control", "decisions", "architecture"],
  "control.paused": ["control", "board", "agents"],
  "control.resumed": ["control", "board", "agents"],
  "capacity.changed": ["capacity"],
  "decision.created": ["decisions"],
  "decision.updated": ["decisions"],
  "decision.resolved": ["decisions"],
  "decisions.updated": ["decisions"],
  "architecture.updated": ["architecture"],
  "conversation.message": ["chat"],
  "conversation.updated": ["chat"],
  "hillclimb.updated": ["metrics"],
  "hillclimb.attempt": ["metrics"],
} as const;

export function useLive(topics: string) {
  const [revisions, setRevisions] = useState<Revisions>({
    board: 0, agents: 0, thread: 0, questions: 0, plans: 0, briefs: 0, control: 0, capacity: 0,
    decisions: 0, architecture: 0, chat: 0, metrics: 0,
  });
  const [recent, setRecent] = useState<Record<string, Message>>({});
  const [connected, setConnected] = useState(false);

  useEffect(() => {
    let stream: EventSource | undefined;
    let lastId = "";
    let retry: ReturnType<typeof setTimeout> | undefined;
    let delay = 500;
    let closed = false;
    const pending = new Set<Scope>();
    const messages = new Map<string, Message>();
    const taskStatuses = new Map<string, string>();
    let timer: ReturnType<typeof setTimeout> | undefined;

    function invalidate(changed: readonly Scope[]) {
      changed.forEach((scope) => pending.add(scope));
      if (timer) return;
      timer = setTimeout(() => {
        const refreshed = Array.from(pending);
        const updates = Object.fromEntries(messages);
        pending.clear();
        messages.clear();
        timer = undefined;
        setRevisions((current) => {
          const next = { ...current };
          refreshed.forEach((scope) => { next[scope] += 1; });
          return next;
        });
        if (Object.keys(updates).length) setRecent((current) => ({ ...current, ...updates }));
      }, 650);
    }

    function connect() {
      const params = new URLSearchParams({ topics });
      if (lastId) params.set("last_event_id", lastId);
      const source = new EventSource(`/api/v1/events?${params}`);
      stream = source;
      const resumed = lastId !== "";

      function on(event: string, handler: (event: MessageEvent<string>) => void) {
        source.addEventListener(event, (e) => {
          const message = e as MessageEvent<string>;
          if (message.lastEventId) lastId = message.lastEventId;
          handler(message);
        });
      }

      on("hello", () => {
        delay = 500;
        setConnected(true);
        if (!resumed) invalidate(scopes);
      });
      on("resync", () => invalidate(scopes));
      on("lagged", () => invalidate(scopes));
      source.onerror = () => {
        setConnected(false);
        source.close();
        if (closed || retry) return;
        retry = setTimeout(() => {
          retry = undefined;
          connect();
        }, delay);
        delay = Math.min(delay * 2, 15000);
      };
      on("message.created", (event) => {
        try {
          const { data } = JSON.parse(event.data) as { data?: Message };
          if (data?.thread_kind && data.thread_id) messages.set(`${data.thread_kind}:${data.thread_id}`, data);
        } catch {}
        invalidate(["thread", "chat"]);
      });
      on("task.updated", (event) => {
        invalidate(["board"]);
        try {
          const { data } = JSON.parse(event.data) as { data?: { id: string; status: string } };
          if (data && taskStatuses.get(data.id) !== data.status) {
            taskStatuses.set(data.id, data.status);
            invalidate(["capacity", "decisions"]);
          }
        } catch {}
      });
      Object.entries(eventScopes).forEach(([event, changed]) => {
        on(event, () => invalidate(changed));
      });
    }

    connect();
    return () => {
      closed = true;
      if (timer) clearTimeout(timer);
      if (retry) clearTimeout(retry);
      stream?.close();
    };
  }, [topics]);

  return { revisions, recent, connected };
}
