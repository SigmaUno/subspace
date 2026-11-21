import type { Artifact } from "./generated-types";

export type { Artifact };

export type ArtifactList = { artifacts: Artifact[] };

export type ScopeKind = "agent" | "task" | "brief";

export type ScopeOptions = {
  agents?: { id: string }[];
  tasks?: { id: string; title?: string }[];
  briefs?: { id: string; title?: string }[];
};
