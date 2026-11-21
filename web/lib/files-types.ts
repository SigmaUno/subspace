export type Artifact = {
  id: string;
  scope: string;
  name: string;
  size: number;
  sha256: string;
  content_type: string;
  uploaded_by: string;
  project: string;
  created_at: string;
};

export type ArtifactList = { artifacts: Artifact[] };

export type ScopeKind = "agent" | "task" | "brief";

export type ScopeOptions = {
  agents?: { id: string }[];
  tasks?: { id: string; title?: string }[];
  briefs?: { id: string; title?: string }[];
};
