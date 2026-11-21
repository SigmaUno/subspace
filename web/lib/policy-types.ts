import type { Policy, PolicyDecision, PolicyFact, PolicyFinding, PolicyHold, PolicyVersion } from "./generated-types";

export type {
  Policy, PolicyDecision, PolicyDocument, PolicyDryRunInput, PolicyDryRunResult, PolicyFact, PolicyFinding, PolicyHold,
  PolicyMatch, PolicyVersion,
} from "./generated-types";

export type PolicyList = { policies: Policy[] | null };
export type PolicyDecisionList = { decisions: PolicyDecision[] | null };
export type PolicyHoldList = { holds: PolicyHold[] | null };
export type PolicyVersionList = { versions: PolicyVersion[] | null };
export type PolicyCatalog = { facts: PolicyFact[] | null; actions: string[] | null; effects: string[] | null };
export type PolicyLint = { ok: boolean; findings: PolicyFinding[] | null };
