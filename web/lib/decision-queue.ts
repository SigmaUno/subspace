import type { Decision } from "./swarm";
import { safeURL } from "./text.ts";
import type { Thread } from "./types";

export function orderDecisions(decisions: readonly Decision[], resolved: readonly string[]): Decision[] {
  return decisions
    .filter((item) => !resolved.includes(item.id))
    .sort((a, b) => (b.impact ?? 0) - (a.impact ?? 0) || a.created_at.localeCompare(b.created_at));
}

export function pickDecision(
  eligible: readonly Decision[],
  selected: string | undefined,
  serverNext: Decision | null | undefined,
  resolved: readonly string[],
  skipped: readonly string[],
): Decision | undefined {
  return eligible.find((item) => item.id === selected) ??
    (serverNext && !resolved.includes(serverNext.id) && !skipped.includes(serverNext.id) ? serverNext : eligible[0]);
}

export function decisionThread(item: Pick<Decision, "task_id" | "agent_id" | "brief_id">): Thread | null {
  return item.task_id ? { kind: "task", id: item.task_id }
    : item.agent_id ? { kind: "agent", id: item.agent_id }
    : item.brief_id ? { kind: "brief", id: item.brief_id } : null;
}

export function diffURL(url: string | undefined): URL | null {
  const safe = safeURL(url);
  if (!safe) return null;
  const parsed = new URL(safe);
  if (/\/pulls?\/\d+\/?$/.test(parsed.pathname)) parsed.pathname = parsed.pathname.replace(/\/$/, "") + "/files";
  return parsed;
}
