export interface SavedView {
  name: string;
  query: string;
}

export function parseList(raw: string | null): string[] {
  try {
    const value: unknown = JSON.parse(raw ?? "[]");
    return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
  } catch {
    return [];
  }
}

export function parseViews(raw: string | null): SavedView[] {
  try {
    const value: unknown = JSON.parse(raw ?? "[]");
    if (!Array.isArray(value)) return [];
    return value.filter((item): item is SavedView => typeof item?.name === "string" && typeof item?.query === "string");
  } catch {
    return [];
  }
}

export function saveView(views: SavedView[], name: string, query: string): SavedView[] {
  return [...views.filter((view) => view.name !== name), { name, query }];
}

export function removeView(views: SavedView[], name: string): SavedView[] {
  return views.filter((view) => view.name !== name);
}

export function toggleItem(items: ReadonlySet<string>, item: string): string[] {
  return items.has(item) ? [...items].filter((entry) => entry !== item) : [...items, item];
}
