export type KnowledgeKind = "decision" | "convention" | "pitfall" | "howto" | "glossary";

export type KnowledgeStatus = "pending" | "active" | "superseded";

export type KnowledgeEntry = {
  id: string;
  brief_id: string;
  kind: KnowledgeKind;
  title: string;
  body: string;
  tags: string[];
  author: string;
  author_role: string;
  status: KnowledgeStatus;
  pinned: boolean;
  supersedes: string | null;
  superseded_by: string | null;
  created_at: string;
  updated_at: string;
};

export type KnowledgeList = { entries: KnowledgeEntry[] };
