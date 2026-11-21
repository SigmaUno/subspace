"use client";

import { useEffect, useRef, useState } from "react";
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

export type LiveStatus = "connected" | "reconnecting" | "offline";

const retryDelay = 5_000;

export function useLive(topics: string) {
  const [revisions, setRevisions] = useState<Revisions>({
    board: 0, agents: 0, thread: 0, questions: 0, plans: 0, briefs: 0, control: 0, capacity: 0,
    decisions: 0, architecture: 0, chat: 0, metrics: 0,
  });
  const [recent, setRecent] = useState<Record<string, Message>>({});
  const [status, setStatus] = useState<LiveStatus>("reconnecting");
  const [attempt, setAttempt] = useState(0);
  const resume = useRef({ topics, id: "" });

  useEffect(() => {
    if (resume.current.topics !== topics) resume.current = { topics, id: "" };
    const cursor = resume.current;
    const resumed = cursor.id !== "";
    const params = new URLSearchParams({ topics });
    if (resumed) params.set("last_event_id", cursor.id);
    const stream = new EventSource(`/api/v1/events?${params}`);
    let opened = resumed;
    const pending = new Set<Scope>();
    const messages = new Map<string, Message>();
    const taskStatuses = new Map<string, string>();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let reconnect: ReturnType<typeof setTimeout> | undefined;

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

    function on(event: string, handler: (event: MessageEvent<string>) => void) {
      stream.addEventListener(event, (e) => {
        const message = e as MessageEvent<string>;
        if (message.lastEventId) cursor.id = message.lastEventId;
        handler(message);
      });
    }

    on("hello", () => {
      setStatus("connected");
      if (!opened) invalidate(scopes);
      opened = true;
    });
    on("resync", () => invalidate(scopes));
    on("lagged", () => invalidate(scopes));
    stream.onerror = () => {
      if (stream.readyState === EventSource.CLOSED) {
        stream.close();
        setStatus("offline");
        reconnect = setTimeout(() => setAttempt((count) => count + 1), retryDelay);
      } else {
        setStatus(navigator.onLine ? "reconnecting" : "offline");
      }
    };
    const online = () => setStatus("reconnecting");
    const offline = () => setStatus("offline");
    window.addEventListener("online", online);
    window.addEventListener("offline", offline);
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
    return () => {
      if (timer) clearTimeout(timer);
      if (reconnect) clearTimeout(reconnect);
      window.removeEventListener("online", online);
      window.removeEventListener("offline", offline);
      stream.close();
    };
  }, [topics, attempt]);

  return { revisions, recent, connected: status === "connected", status };
}
