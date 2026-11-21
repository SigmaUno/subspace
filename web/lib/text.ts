export function commaList(input: string) {
  return input.split(/[,\n]/).map((entry) => entry.trim()).filter(Boolean);
}

export const label = (status: string) => status.replaceAll("_", " ");

export function safeURL(input?: string | null): string | undefined {
  if (!input) return undefined;
  try {
    const url = new URL(input);
    if (url.protocol === "https:" || url.protocol === "http:") return url.href;
  } catch {
  }
}
