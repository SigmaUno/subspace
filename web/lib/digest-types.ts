type DigestRisk = {
  source: "spend" | "forecast";
  severity: "high" | "medium";
  subject: string;
  message: string;
};

export type Digest = {
  since: string;
  until: string;
  tasks: { done: number; created: number; blocked: number };
  cost_eur: number;
  tokens: number;
  risks: DigestRisk[] | null;
  decisions: { count: number; titles: string[] | null };
  flapping_agents: string[] | null;
  merge_ready: string[] | null;
};

export type DigestPreview = { digest: Digest; title: string; text: string };
