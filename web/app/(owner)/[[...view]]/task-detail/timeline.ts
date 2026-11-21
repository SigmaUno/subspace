import { label } from "../../../../lib/client";
import type { AuditEvent, TimelineEntry } from "../../../../lib/task-detail-types";
import { statuses, type Message } from "../../../../lib/types";

const statusNames = new Map<string, string>(statuses);

const stateOf = (state: AuditEvent["before"]) => typeof state?.status === "string" ? state.status : undefined;
export const statusName = (status: string) => statusNames.get(status) ?? label(status);

export function timeline(events: AuditEvent[], messages: Message[]): TimelineEntry[] {
  const entries: TimelineEntry[] = events.map((event) => {
    const before = stateOf(event.before);
    const after = stateOf(event.after);
    return {
      key: `audit-${event.id}`,
      at: event.ts,
      actor: event.agent_id || event.role,
      title: after && before !== after
        ? `${before ? statusName(before) : "New"} → ${statusName(after)}`
        : label(event.tool),
      detail: event.note ?? undefined,
    };
  });
  for (const message of messages) {
    if (message.kind === "message" || message.kind === "status") continue;
    entries.push({
      key: `message-${message.id}`,
      at: message.created_at,
      actor: message.author_id,
      title: label(message.kind),
      detail: message.body,
    });
  }
  return entries.sort((a, b) => Date.parse(a.at) - Date.parse(b.at) || a.key.localeCompare(b.key));
}
