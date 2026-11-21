export const tourStorageKey = "subspace.tour";

const completed = "completed";
const listeners = new Set<() => void>();

export type TourStep = { title: string; body: string; target?: string };

export const tourSteps: TourStep[] = [
  { title: "Overview", body: "The swarm view shows agents, tasks and live activity for your briefs.", target: "overview" },
  { title: "Decisions inbox", body: "Plans, questions and alerts that need you wait here. Approve, answer or open the thread.", target: "decisions" },
  { title: "Board", body: "Briefs and their kanban boards. Each task sits in the column of its status.", target: "nav-briefs" },
  { title: "Agents and bots", body: "Register and monitor leaders, workers and testers, and review the bots.", target: "nav-agents" },
  { title: "Plan approvals", body: "The leader proposes a plan for each brief. Approve it to release its tasks; find Plans under More.", target: "more" },
  { title: "Settings hub", body: "Notifications, backups, tokens, policies and schedules live in Settings under More.", target: "more" },
  { title: "Command palette", body: "Press Ctrl+K anywhere to search and jump to any page or item." },
  { title: "Mission control", body: "A live operations view of what the swarm is doing right now; find it under More.", target: "more" },
];

export function subscribeTour(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

export function readTourCompleted() {
  try {
    return localStorage.getItem(tourStorageKey) === completed;
  } catch {
    return false;
  }
}

export function completeTour() {
  try {
    localStorage.setItem(tourStorageKey, completed);
  } catch {
  }
  listeners.forEach((listener) => listener());
}
