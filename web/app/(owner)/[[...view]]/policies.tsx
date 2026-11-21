"use client";

import { useState } from "react";
import { api, optionalResource, useResource, useSubmit, value } from "../../../lib/client";
import type {
  Policy, PolicyCatalog, PolicyDecisionList, PolicyDocument, PolicyDryRunInput, PolicyDryRunResult, PolicyFinding,
  PolicyHold, PolicyHoldList, PolicyLint, PolicyList, PolicyMatch,
} from "../../../lib/policy-types";
import { Feedback } from "./components";
import { formatTime } from "./format";
import { EmptyState, ResourceState } from "./states";
import { useAction, useConfirm } from "./ui";

const starter: PolicyDocument = {
  name: "Deploy changes need the owner",
  description: "Assignments of tasks that touch deploy/** wait for owner approval.",
  enabled: true,
  timezone: "UTC",
  rules: [{
    id: "deploy",
    when: { actions: ["assign"], match: "overlaps(task.paths, \"deploy/**\")" },
    then: { effect: "require_owner_approval", timeout: "24h", message: "task touches deploy/**" },
  }],
};

const outcomeClass: Record<string, string> = { deny: "revoked", hold: "paused", warn: "paused", approved: "active", allow: "active" };

function documentOf(policy: Policy): PolicyDocument {
  return { name: policy.name, description: policy.description, enabled: policy.enabled, timezone: policy.timezone, rules: policy.rules };
}

function parseDocument(text: string): PolicyDocument {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (cause) {
    throw new Error(`The policy is not valid JSON: ${cause instanceof Error ? cause.message : "parse error"}`);
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("The policy must be a JSON object.");
  return parsed as PolicyDocument;
}

function Findings({ findings }: { findings: PolicyFinding[] | null }) {
  if (!findings?.length) return <p className="meta" role="status">No lint findings.</p>;
  return <ul className="stack" role="status" aria-label="Lint findings">
    {findings.map((finding, index) => <li key={`${finding.code}:${finding.rule ?? ""}:${index}`}>
      <span className={`pill ${finding.severity === "error" ? "revoked" : "paused"}`}>{finding.severity}</span>{" "}
      <code>{finding.code}</code>{finding.rule && <> in <code>{finding.rule}</code></>}: {finding.message}
    </li>)}
  </ul>;
}

function Matches({ matched }: { matched: PolicyMatch[] | null }) {
  if (!matched?.length) return <p className="meta">No rule matched.</p>;
  return <div className="table-wrap">
    <table>
      <thead><tr><th>Policy</th><th>Rule</th><th>Effect</th><th>Message</th></tr></thead>
      <tbody>{matched.map((match) => <tr key={`${match.policy_id}:${match.rule}`}>
        <th scope="row">{match.policy} <span className="meta">v{match.version}</span></th>
        <td><code>{match.rule}</code></td>
        <td>{match.effect}{!match.applied && <span className="meta"> · satisfied</span>}</td>
        <td>{match.message}</td>
      </tr>)}</tbody>
    </table>
  </div>;
}

function Facts({ facts }: { facts: Record<string, unknown> | null }) {
  const entries = Object.entries(facts ?? {}).sort(([a], [b]) => a.localeCompare(b));
  if (!entries.length) return null;
  return <dl className="stack" aria-label="Facts used">
    {entries.map(([name, fact]) => <div key={name} className="heading"><dt><code>{name}</code></dt><dd><code>{JSON.stringify(fact)}</code></dd></div>)}
  </dl>;
}

function FactReference({ catalog }: { catalog?: PolicyCatalog | null }) {
  if (!catalog?.facts?.length) return null;
  return <details>
    <summary>Facts, actions and effects</summary>
    <p className="meta">Actions: {(catalog.actions ?? []).join(", ")}. Effects: {(catalog.effects ?? []).join(", ")}.</p>
    <div className="table-wrap">
      <table>
        <thead><tr><th>Fact</th><th>Type</th><th>Meaning</th></tr></thead>
        <tbody>{catalog.facts.map((fact) => <tr key={fact.name}>
          <th scope="row"><code>{fact.name}</code></th><td>{fact.type}</td><td>{fact.description}</td>
        </tr>)}</tbody>
      </table>
    </div>
  </details>;
}

function PolicyEditor({ policy, catalog, draft, setDraft, done, cancel }: {
  policy?: Policy; catalog?: PolicyCatalog | null; draft: string; setDraft: (text: string) => void; done: () => void; cancel: () => void;
}) {
  const form = useSubmit();
  const [findings, setFindings] = useState<PolicyFinding[] | null>(policy?.warnings ?? null);
  const [linted, setLinted] = useState(false);
  const [lintError, setLintError] = useState("");

  async function lint() {
    setLintError("");
    try {
      const result = await api<PolicyLint>("policies/lint", "POST", parseDocument(draft));
      setFindings(result.findings);
      setLinted(true);
    } catch (cause) {
      setLintError(cause instanceof Error ? cause.message : "Request failed.");
    }
  }

  return <form className="panel stack" data-write onSubmit={(event) => form.submit(event, async () => {
    const body = parseDocument(draft);
    if (policy) await api<Policy>(`policies/${encodeURIComponent(policy.id)}`, "PUT", body);
    else await api<Policy>("policies", "POST", body);
    done();
  })}>
    <h3>{policy ? `Edit ${policy.name} (version ${policy.version})` : "New policy"}</h3>
    <label>Policy document (JSON)
      <textarea name="document" rows={18} spellCheck={false} value={draft} onChange={(event) => setDraft(event.target.value)} />
    </label>
    <FactReference catalog={catalog} />
    {(linted || !!findings?.length) && <Findings findings={findings} />}
    <Feedback error={lintError || form.error} />
    <div className="actions">
      <button className="primary" disabled={form.busy}>{policy ? "Save new version" : "Create policy"}</button>
      <button type="button" onClick={lint}>Lint</button>
      <button type="button" onClick={cancel}>Close</button>
    </div>
  </form>;
}

function DryRun({ draft }: { draft: string }) {
  const form = useSubmit();
  const [result, setResult] = useState<PolicyDryRunResult>();
  return <section className="panel stack" aria-labelledby="policy-dry-run">
    <h3 id="policy-dry-run">Dry run</h3>
    <p className="meta">Evaluates a sample action against live data without changing anything or recording a decision.</p>
    <form className="stack" onSubmit={(event) => form.submit(event, async (fields) => {
      const input: PolicyDryRunInput = { action: value(fields, "action") };
      for (const key of ["actor_id", "task_id", "agent_id", "brief_id", "plan_id"] as const) {
        const text = value(fields, key);
        if (text) input[key] = text;
      }
      const at = value(fields, "at");
      if (at) input.at = new Date(at).toISOString();
      const facts = value(fields, "facts");
      if (facts) {
        try {
          input.facts = JSON.parse(facts) as Record<string, unknown>;
        } catch {
          throw new Error("Fact overrides must be a JSON object such as {\"time.weekday\": \"sat\"}.");
        }
      }
      if (fields.get("use_draft") === "on") input.policy = parseDocument(draft);
      setResult(await api<PolicyDryRunResult>("policies/evaluate", "POST", input));
    })}>
      <div className="form-grid">
        <label>Action<select name="action" defaultValue="assign">
          {["assign", "claim_task", "submit", "review", "request_spawn", "create_task", "update_task", "approve_plan", "merge_ready"]
            .map((action) => <option key={action} value={action}>{action}</option>)}
        </select></label>
        <label>Actor agent<input name="actor_id" placeholder="owner" /></label>
        <label>Task<input name="task_id" placeholder="T-1" /></label>
        <label>Target agent<input name="agent_id" placeholder="claude-1" /></label>
        <label>Brief<input name="brief_id" placeholder="B-1" /></label>
        <label>Plan<input name="plan_id" placeholder="P-1" /></label>
        <label>At<input name="at" type="datetime-local" /></label>
      </div>
      <label>Fact overrides (JSON)<textarea name="facts" rows={2} spellCheck={false} placeholder={"{\"time.weekday\": \"sat\"}"} /></label>
      <label className="inline"><input type="checkbox" name="use_draft" /> Evaluate only the policy in the editor</label>
      <Feedback error={form.error} />
      <div className="actions"><button className="primary" disabled={form.busy}>Evaluate</button></div>
    </form>
    {result && <div className="stack" role="status" aria-label="Dry run result">
      <p><span className={`pill ${outcomeClass[result.outcome] ?? ""}`}>{result.outcome}</span> {result.message}
        <span className="meta"> · {result.steps} steps · at {formatTime(result.at)}</span></p>
      <Matches matched={result.matched} />
      <Facts facts={result.facts} />
      {!!result.errors?.length && <ul>{result.errors.map((error) => <li key={error} className="error">{error}</li>)}</ul>}
    </div>}
  </section>;
}

function holdSubject(hold: PolicyHold) {
  const subject = hold.task_id || hold.plan_id || hold.brief_id;
  return `${hold.action}${subject ? ` on ${subject}` : ""}${hold.target ? ` → ${hold.target}` : ""}`;
}

function Holds({ revision, changed }: { revision: number; changed: () => void }) {
  const holds = useResource<PolicyHoldList>("policy-holds?limit=50", revision);
  const action = useAction();
  const confirm = useConfirm();
  const list = holds.data?.holds ?? [];

  async function decide(hold: PolicyHold, verb: "approve" | "reject") {
    const accepted = await confirm({
      title: `${verb === "approve" ? "Approve" : "Reject"} ${hold.id}`,
      message: `${holdSubject(hold)} by ${hold.actor_id}: ${hold.message}`,
      confirm: verb === "approve" ? "Approve" : "Reject",
      danger: verb === "reject",
    });
    if (!accepted) return;
    const ok = await action.run(`${verb}:${hold.id}`, () => api<PolicyHold>(`policy-holds/${encodeURIComponent(hold.id)}/${verb}`, "POST", {}),
      `${hold.id} ${verb === "approve" ? "approved" : "rejected"}.`);
    if (ok) {
      holds.refresh();
      changed();
    }
  }

  return <section className="panel stack" aria-labelledby="policy-holds">
    <h3 id="policy-holds">Held actions</h3>
    <ResourceState error={holds.error} loading={holds.data === undefined} retry={holds.refresh} rows={2} />
    {holds.data && !list.length && <EmptyState>No action has been held for approval.</EmptyState>}
    {!!list.length && <div className="table-wrap">
      <table>
        <thead><tr><th>Hold</th><th>Action</th><th>Status</th><th>Expires</th><th>Decide</th></tr></thead>
        <tbody>{list.map((hold) => <tr key={hold.id}>
          <th scope="row">{hold.id}<div className="meta">{formatTime(hold.created_at)}</div></th>
          <td>{holdSubject(hold)}<div className="meta">by {hold.actor_id} · {hold.message}</div></td>
          <td><span className={`pill ${hold.status === "pending" ? "paused" : hold.status === "rejected" || hold.status === "expired" ? "revoked" : "active"}`}>{hold.status}</span>
            {hold.result && <div className="meta">{hold.result}</div>}</td>
          <td>{formatTime(hold.expires_at)}</td>
          <td>{hold.status === "pending" && <div className="actions" data-write>
            <button className="primary" disabled={action.busy !== ""} onClick={() => decide(hold, "approve")}>Approve</button>
            <button className="danger" disabled={action.busy !== ""} onClick={() => decide(hold, "reject")}>Reject</button>
          </div>}</td>
        </tr>)}</tbody>
      </table>
    </div>}
  </section>;
}

function History({ revision }: { revision: number }) {
  const [outcome, setOutcome] = useState("");
  const query = new URLSearchParams({ limit: "100" });
  if (outcome) query.set("outcome", outcome);
  const decisions = useResource<PolicyDecisionList>(`policies/decisions?${query}`, revision);
  const list = decisions.data?.decisions ?? [];
  return <section className="panel stack" aria-labelledby="policy-history">
    <div className="heading"><h3 id="policy-history">Decision history</h3>
      <label>Outcome<select value={outcome} onChange={(event) => setOutcome(event.target.value)}>
        <option value="">All</option>
        {["deny", "hold", "warn", "approved", "allow"].map((name) => <option key={name} value={name}>{name}</option>)}
      </select></label>
    </div>
    <ResourceState error={decisions.error} loading={decisions.data === undefined} retry={decisions.refresh} rows={3} />
    {decisions.data && !list.length && <EmptyState>No policy has decided anything yet.</EmptyState>}
    {!!list.length && <div className="table-wrap">
      <table>
        <thead><tr><th>When</th><th>Action</th><th>Outcome</th><th>Rules</th></tr></thead>
        <tbody>{list.map((decision) => <tr key={decision.id}>
          <th scope="row">{formatTime(decision.ts)}<div className="meta">{decision.id}</div></th>
          <td>{decision.action}{decision.task_id && ` on ${decision.task_id}`}{decision.target && ` → ${decision.target}`}
            <div className="meta">by {decision.actor_id}</div></td>
          <td><span className={`pill ${outcomeClass[decision.outcome] ?? ""}`}>{decision.outcome}</span>
            <div className="meta">{decision.message}{decision.hold_id && ` · ${decision.hold_id}`}</div></td>
          <td>{(decision.matched ?? []).filter((match) => match.applied).map((match) => `${match.policy}/${match.rule}`).join(", ") || "—"}</td>
        </tr>)}</tbody>
      </table>
    </div>}
  </section>;
}

export function PoliciesPanel() {
  const policies = useResource<PolicyList>("policies", 0);
  const catalog = useResource("policies/facts", 0, optionalResource<PolicyCatalog>);
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState(JSON.stringify(starter, null, 2));
  const [revision, setRevision] = useState(0);
  const [error, setError] = useState("");
  const action = useAction();
  const confirm = useConfirm();
  const list = policies.data?.policies ?? [];
  const current = list.find((policy) => policy.id === editing);

  function open(policy?: Policy) {
    setEditing(policy ? policy.id : "");
    setDraft(JSON.stringify(policy ? documentOf(policy) : starter, null, 2));
  }

  function finish() {
    setEditing(null);
    policies.refresh();
    setRevision((count) => count + 1);
  }

  async function toggle(policy: Policy) {
    const ok = await action.run(`toggle:${policy.id}`,
      () => api<Policy>(`policies/${encodeURIComponent(policy.id)}`, "PUT", { ...documentOf(policy), enabled: !policy.enabled }),
      `${policy.name} ${policy.enabled ? "disabled" : "enabled"}.`);
    if (ok) policies.refresh();
  }

  async function remove(policy: Policy) {
    const accepted = await confirm({ title: `Delete ${policy.name}`, message: "The policy and its versions are deleted; its decisions stay.", confirm: "Delete", danger: true });
    if (!accepted) return;
    setError("");
    try {
      await api<void>(`policies/${encodeURIComponent(policy.id)}`, "DELETE");
      if (editing === policy.id) setEditing(null);
      policies.refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Request failed.");
    }
  }

  return <section className="stack" aria-labelledby="policies-heading">
    <div className="heading"><h2 id="policies-heading">Policies</h2>
      <button data-write onClick={() => (editing === "" ? setEditing(null) : open())}>{editing === "" ? "Close editor" : "New policy"}</button></div>
    <p className="meta">Rules on assign, claim_task, submit, review, request_spawn, create_task, update_task, approve_plan and merge_ready.
      Denied and held actions change nothing.</p>
    <ResourceState error={policies.error} loading={policies.data === undefined} retry={policies.refresh} rows={3} />
    {editing === "" && <PolicyEditor key="new" catalog={catalog.data} draft={draft} setDraft={setDraft} done={finish} cancel={() => setEditing(null)} />}
    {current && <PolicyEditor key={`${current.id}:${current.version}`} policy={current} catalog={catalog.data} draft={draft} setDraft={setDraft}
      done={finish} cancel={() => setEditing(null)} />}
    <Feedback error={error} />
    {policies.data && !list.length && <EmptyState>No policies. The fixed board rules apply.</EmptyState>}
    {!!list.length && <div className="table-wrap">
      <table>
        <thead><tr><th>Policy</th><th>Rules</th><th>Lint</th><th>Actions</th></tr></thead>
        <tbody>{list.map((policy) => <tr key={policy.id}>
          <th scope="row">{policy.name} <span className={`pill ${policy.enabled ? "active" : ""}`}>{policy.enabled ? "enabled" : "disabled"}</span>
            <div className="meta">{policy.id} · version {policy.version} · {policy.timezone} · updated {formatTime(policy.updated_at)}</div></th>
          <td>{(policy.rules ?? []).map((rule) => <div key={rule.id}><code>{rule.id}</code>
            <span className="meta"> {(rule.when.actions ?? []).join(", ")} → {rule.then.effect}</span></div>)}</td>
          <td>{policy.warnings?.length ? `${policy.warnings.length} warnings` : "clean"}</td>
          <td><div className="actions" data-write>
            <button onClick={() => open(policy)}>Edit</button>
            <button disabled={action.busy !== ""} onClick={() => toggle(policy)}>{policy.enabled ? "Disable" : "Enable"}</button>
            <button className="danger" onClick={() => remove(policy)}>Delete</button>
          </div></td>
        </tr>)}</tbody>
      </table>
    </div>}
    <DryRun draft={draft} />
    <Holds revision={revision} changed={() => setRevision((count) => count + 1)} />
    <History revision={revision} />
  </section>;
}
