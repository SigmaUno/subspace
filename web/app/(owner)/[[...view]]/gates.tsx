"use client";

import { api, optionalResource, useResource, useSubmit, value } from "../../../lib/client";
import { Feedback } from "./components";
import { EmptyState } from "./states";

export type GateResult = { gate: string; enabled: boolean; passed: boolean; overridden: boolean; evidence: string };

export type GateOverride = { gates: string[] | null; reason: string; by: string; at: string };

export type GateReport = {
  task_id: string;
  brief_id: string;
  passed: boolean;
  gates: GateResult[] | null;
  override: GateOverride | null;
  evaluated_at: string;
};

const gateNames: Record<string, string> = {
  tests: "Tests",
  checks: "PR checks",
  scope: "Scope",
  questions: "Owner questions",
  review: "Cross-provider review",
  budget: "Budget",
  pr_age: "PR age",
};

function gatePath(taskID: string) {
  return `tasks/${encodeURIComponent(taskID)}/gates`;
}

export function useGates(taskID: string | undefined, revision: number) {
  return useResource(taskID ? gatePath(taskID) : null, revision, optionalResource<GateReport>);
}

const enabledGates = (report: GateReport) => (report.gates ?? []).filter((gate) => gate.enabled);

export function GateChecklist({ report }: { report: GateReport }) {
  const gates = enabledGates(report);
  if (!gates.length) return <EmptyState>No quality gates on this brief.</EmptyState>;
  return <ul className="stack" aria-label="Quality gates">
    {gates.map((gate) => {
      const state = gate.passed ? "Pass" : gate.overridden ? "Overridden" : "Fail";
      const tone = gate.passed ? "approved" : gate.overridden ? "changes_requested" : "blocked";
      return <li key={gate.gate}>
        <span className="pill" data-status={tone}>{state}</span> <strong>{gateNames[gate.gate] ?? gate.gate}</strong>{" "}
        <span className="meta break">{gate.evidence}</span>
      </li>;
    })}
  </ul>;
}

function overrideTargets(report: GateReport, failing: GateResult[]) {
  const previous = report.override;
  if (previous && !(previous.gates ?? []).length) return [];
  return Array.from(new Set([...(previous?.gates ?? []), ...failing.map((gate) => gate.gate)]));
}

export function TaskGates({ taskID, revision }: { taskID: string; revision: number }) {
  const gates = useGates(taskID, revision);
  const form = useSubmit();
  const report = gates.data;
  if (!report) return gates.error ? <section className="panel stack"><h2>Quality gates</h2><Feedback error={gates.error} /></section> : null;
  const failing = enabledGates(report).filter((gate) => !gate.passed && !gate.overridden);
  return <section className="panel stack">
    <h2>Quality gates</h2>
    <Feedback error={gates.error} />
    <GateChecklist report={report} />
    {report.override && <p className="meta">Override by {report.override.by}: {report.override.reason}</p>}
    {failing.length > 0 && <form className="stack" data-write onSubmit={(event) => form.submit(event, async (fields, element) => {
      gates.setData(await api<GateReport>(`${gatePath(taskID)}/override`, "POST", {
        reason: value(fields, "reason"),
        gates: overrideTargets(report, failing),
      }));
      element.reset();
    })}>
      <h3>Override failing gates</h3>
      <label>Reason<input name="reason" required maxLength={2000} /></label>
      <Feedback error={form.error} />
      <div><button className="danger" disabled={form.busy}>Override {failing.length === 1 ? "1 gate" : `${failing.length} gates`}</button></div>
    </form>}
  </section>;
}

export function DecisionGates({ taskID }: { taskID: string }) {
  const gates = useGates(taskID, 0);
  if (!gates.data || !enabledGates(gates.data).length) return null;
  return <div className="stack">
    <div className="meta">Quality gates {gates.data.passed ? "passed" : "failing"}</div>
    <GateChecklist report={gates.data} />
  </div>;
}
