export const themes = {
  light: { background: "#f9f9f9", accent: "#178a6c" },
} as const;

export type Theme = keyof typeof themes;

export const theme: Theme = "light";
