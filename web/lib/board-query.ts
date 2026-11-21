import type { Task } from "./types";

export const none = "-";

export const statusGroups = [
  { id: "queue", name: "Queue", statuses: ["backlog", "ready"] },
  { id: "active", name: "Active", statuses: ["assigned", "in_progress"] },
  { id: "review", name: "Review", statuses: ["submitted", "in_review", "changes_requested"] },
  { id: "attention", name: "Attention", statuses: ["blocked", "needs_owner"] },
  { id: "finished", name: "Finished", statuses: ["approved", "done", "cancelled"] },
];

export interface BoardQuery {
  text: string;
  assignee: string;
  role: string;
  group: string;
  component: string;
  priority: string;
  blocked: boolean;
  stale: boolean;
  mergeReady: boolean;
}

export const emptyQuery: BoardQuery = {
  text: "", assignee: "", role: "", group: "", component: "", priority: "", blocked: false, stale: false, mergeReady: false,
};

export function groupStatuses(id: string): readonly string[] | undefined {
  return statusGroups.find((group) => group.id === id)?.statuses;
}

export function parseQuery(search: string): BoardQuery {
  const input = new URLSearchParams(search);
  const priority = input.get("priority") ?? "";
  const group = input.get("group") ?? "";
  return {
    text: input.get("q") ?? "",
    assignee: input.get("assignee") ?? "",
    role: input.get("role") ?? "",
    group: groupStatuses(group) ? group : "",
    component: input.get("component") ?? "",
    priority: /^-?\d{1,9}$/.test(priority) ? priority : "",
    blocked: input.get("blocked") === "1",
    stale: input.get("stale") === "1",
    mergeReady: input.get("merge") === "1",
  };
}

export function formatQuery(query: BoardQuery): string {
  const output = new URLSearchParams();
  const fields = { q: query.text, assignee: query.assignee, role: query.role, group: query.group, component: query.component, priority: query.priority };
  for (const [key, value] of Object.entries(fields)) if (value) output.set(key, value);
  if (query.blocked) output.set("blocked", "1");
  if (query.stale) output.set("stale", "1");
  if (query.mergeReady) output.set("merge", "1");
  return output.toString();
}

export function haystack(task: Task) {
  return [task.id, task.title, task.spec, task.assignee, task.repo, task.branch].join("\n").toLowerCase();
}

export function facets(tasks: Task[]) {
  const assignees = new Set<string>();
  const roles = new Set<string>();
  const priorities = new Set<number>();
  for (const task of tasks) {
    if (task.assignee) assignees.add(task.assignee);
    roles.add(task.role);
    if (task.priority !== undefined) priorities.add(task.priority);
  }
  return {
    assignees: [...assignees].sort(),
    roles: [...roles].sort(),
    priorities: [...priorities].sort((a, b) => b - a),
  };
}

export interface TaskMatcher {
  isStale(task: Task): boolean;
  isMergeReady(id: string): boolean;
}

export function filterTasks(tasks: Task[], haystacks: Map<string, string>, query: BoardQuery, matcher: TaskMatcher): Task[] {
  const needle = query.text.trim().toLowerCase();
  const group = groupStatuses(query.group);
  return tasks.filter((task) => {
    if (group && !group.includes(task.status)) return false;
    if (query.assignee && !(query.assignee === none ? !task.assignee : task.assignee === query.assignee)) return false;
    if (query.component && !(query.component === none ? !task.component_id : task.component_id === query.component)) return false;
    if (query.role && task.role !== query.role) return false;
    if (query.priority && task.priority !== Number(query.priority)) return false;
    if (query.blocked && task.status !== "blocked" && task.status !== "needs_owner") return false;
    if (query.stale && !(task.status === "in_progress" && matcher.isStale(task))) return false;
    if (query.mergeReady && !matcher.isMergeReady(task.id)) return false;
    return !needle || (haystacks.get(task.id) ?? "").includes(needle);
  });
}
