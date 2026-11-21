import type { Brief } from "./types";

export type BriefComponentSpec = {
  name: string;
  description: string;
  paths: string[];
  depends_on: string[];
};

export type BriefPreview = {
  title: string;
  body: string;
  repos: string[];
  max_cost_eur: number;
  max_parallel_agents: number;
  allowed_providers: string[];
  cross_review: boolean;
  task_budget: { max_minutes: number; max_cost_eur: number } | null;
  components: BriefComponentSpec[];
};

export type BriefTemplate = {
  id: string;
  title: string;
  variables: string[];
  markdown: string;
};

export type RenderedBriefTemplate = { id: string; markdown: string };

export type BriefImport = { brief: Brief; components: { id: string; name: string }[] };
