import type { Task, TaskStatus } from "./types";

const staleAfter = 15 * 60_000;

const priority: Partial<Record<TaskStatus, number>> = { needs_owner: 0, blocked: 1, in_progress: 2 };

function heartbeat(task: Task) {
  return task.last_heartbeat_at ? Date.parse(task.last_heartbeat_at) : 0;
}

export function isStalled(task: Task, now: number) {
  if (task.status === "blocked" || task.status === "needs_owner") return true;
  if (task.status !== "in_progress") return false;
  if (task.lease_expires_at) return Date.parse(task.lease_expires_at) <= now;
  return !!task.last_heartbeat_at && now - heartbeat(task) > staleAfter;
}

export function stalledTasks(tasks: Task[], now: number, limit = 5) {
  return tasks
    .filter((task) => isStalled(task, now))
    .sort((a, b) => (priority[a.status] ?? 3) - (priority[b.status] ?? 3) || heartbeat(a) - heartbeat(b) || a.id.localeCompare(b.id))
    .slice(0, limit);
}
