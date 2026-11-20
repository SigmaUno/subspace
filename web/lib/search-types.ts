export type SearchKind = "task" | "message" | "agent" | "brief";

export type SearchResult = {
  kind: SearchKind;
  id: string;
  field: string;
  snippet: string;
  at: string;
};

export type SearchResponse = { results: SearchResult[] };
