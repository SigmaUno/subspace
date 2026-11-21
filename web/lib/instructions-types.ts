export type InstructionKind = "rules" | "style" | "templates";

export type InstructionMeta = {
  scope: string;
  kind: InstructionKind;
  version: number;
  author: string;
  message: string;
  created_at: string;
};

export type InstructionVersion = InstructionMeta & { text: string };

export type InstructionLog = { versions: InstructionMeta[] };

export type InstructionDiff = {
  from: number;
  to: number;
  lines: { op: "equal" | "add" | "delete"; text: string }[];
};
