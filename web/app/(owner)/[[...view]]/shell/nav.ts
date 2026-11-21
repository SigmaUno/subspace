type NavItem = { id: string; name: string; href: string };

export const primaryNav: NavItem[] = [
  { id: "swarm", name: "Swarm", href: "/" },
  { id: "briefs", name: "Briefs", href: "/briefs" },
  { id: "agents", name: "Agents", href: "/agents" },
  { id: "costs", name: "Costs", href: "/costs" },
];

export const moreNav: { name: string; items: NavItem[] }[] = [
  { name: "Work", items: [
    { id: "plans", name: "Plans", href: "/plans" },
    { id: "questions", name: "Questions", href: "/questions" },
    { id: "help", name: "Help requests", href: "/help" },
    { id: "bots", name: "Bots", href: "/bots" },
    { id: "mergequeue", name: "Merge queue", href: "/mergequeue" },
  ] },
  { name: "Insight", items: [
    { id: "usage", name: "Usage", href: "/usage" },
    { id: "trends", name: "Trends", href: "/trends" },
    { id: "audit", name: "Audit", href: "/audit" },
    { id: "logs", name: "Logs", href: "/logs" },
    { id: "hillclimb", name: "Hillclimb", href: "/hillclimb" },
  ] },
  { name: "Knowledge", items: [
    { id: "knowledge", name: "Knowledge", href: "/knowledge" },
    { id: "files", name: "Files", href: "/files" },
    { id: "instructions", name: "Instructions", href: "/instructions" },
    { id: "prompts", name: "Prompts", href: "/prompts" },
    { id: "charter", name: "Charter", href: "/charter" },
  ] },
  { name: "Admin", items: [
    { id: "capacity", name: "Capacity", href: "/capacity" },
    { id: "settings", name: "Settings", href: "/settings" },
  ] },
];

export const tabNav = primaryNav.slice(0, 3);

const names = new Map<string, string>([
  ...primaryNav.map((item): [string, string] => [item.id, item.name]),
  ...moreNav.flatMap((group) => group.items.map((item): [string, string] => [item.id, item.name])),
]);

const briefViews: Record<string, string> = { report: "Report", graph: "Task graph", replay: "Replay", architecture: "Architecture" };

export function pageTitle(view: string[]) {
  const [root, id, subview] = view;
  if (!root) return "Swarm";
  if (id && root === "briefs") return [`Brief ${id}`, briefViews[subview]].filter(Boolean).join(" · ");
  if (id && root === "agents") return `Agent ${id}`;
  if (id && root === "tasks") return `Task ${id}`;
  return names.get(root) ?? "Subspace";
}
