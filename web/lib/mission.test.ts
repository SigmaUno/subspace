import assert from "node:assert/strict";
import { test } from "node:test";
import { stalledTasks } from "./mission.ts";
import type { Task } from "./types.ts";

const now = Date.parse("2026-11-21T12:00:00Z");

function task(fields: Partial<Task>): Task {
  return {
    id: "T-1", title: "task", spec: "", acceptance_criteria: null, repo: "o/r", branch: "sub/t-1", role: "worker",
    assignee: "claude-1", reviewer: null, depends_on: null, status: "in_progress", progress_pct: 0,
    last_heartbeat_at: "2026-11-21T11:59:00Z", lease_expires_at: "2026-11-21T12:10:00Z", pr_url: null,
    budget: { max_minutes: 0, max_cost_eur: 0 }, cost_used: 0, minutes_used: 0, blocked_reason: null,
    ...fields,
  } as Task;
}

test("stalled tasks are blocked, owner-needed or with an expired lease or silent heartbeat", () => {
  const tasks = [
    task({ id: "T-1", status: "ready" }),
    task({ id: "T-2", status: "done" }),
    task({ id: "T-3" }),
    task({ id: "T-4", lease_expires_at: "2026-11-21T11:59:00Z" }),
    task({ id: "T-5", lease_expires_at: null, last_heartbeat_at: "2026-11-21T11:30:00Z" }),
    task({ id: "T-6", status: "blocked" }),
    task({ id: "T-7", status: "needs_owner" }),
  ];
  assert.deepEqual(stalledTasks(tasks, now).map((item) => item.id), ["T-7", "T-6", "T-5", "T-4"]);
});

test("stalled tasks list the oldest heartbeat first within a status and honour the limit", () => {
  const tasks = [
    task({ id: "T-1", lease_expires_at: "2026-11-21T11:00:00Z", last_heartbeat_at: "2026-11-21T10:00:00Z" }),
    task({ id: "T-2", lease_expires_at: "2026-11-21T11:00:00Z", last_heartbeat_at: "2026-11-21T09:00:00Z" }),
    task({ id: "T-3", lease_expires_at: "2026-11-21T11:00:00Z", last_heartbeat_at: "2026-11-21T11:30:00Z" }),
  ];
  assert.deepEqual(stalledTasks(tasks, now, 2).map((item) => item.id), ["T-2", "T-1"]);
});
