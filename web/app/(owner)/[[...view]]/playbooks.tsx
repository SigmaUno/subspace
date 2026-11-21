"use client";

import { useState } from "react";
import { api, optionalResource, useResource, useSubmit, value } from "../../../lib/client";
import type { Playbook, PlaybookCriteriaRule, PlaybookList, PlaybookRequiredTask } from "../../../lib/playbooks-types";
import { Feedback } from "./components";
import { EmptyState, ResourceState } from "./states";

const sizes = [["s", "S (60 min)"], ["m", "M (180 min)"], ["l", "L (480 min)"], ["xl", "XL (over 480 min)"]];
const coverage = [["", "No requirement"], ["tester", "Tester task required"], ["tester_or_reviewer", "Tester task or cross review"]];
const roles = ["worker", "tester"];
const severities = ["error", "warning"];

export function usePlaybooks() {
  const resource = useResource("playbooks", 0, optionalResource<PlaybookList>);
  return { ...resource, list: resource.data?.playbooks ?? [] };
}

export function playbookSummary(playbook: Playbook) {
  const parts = [playbook.description];
  if (playbook.coverage) parts.push(playbook.coverage === "tester" ? "every task needs a tester task" : "tester task or cross review");
  if (playbook.allowed_sizes?.length) parts.push(`sizes ${playbook.allowed_sizes.join(", ")}`);
  if (playbook.brief_cost_multiplier) parts.push(`budget ×${playbook.brief_cost_multiplier}`);
  if (playbook.cross_review) parts.push("cross review forced");
  return parts.filter(Boolean).join(" · ");
}

export function PlaybookPicker({ playbooks, selected, onChange }: {
  playbooks: Playbook[]; selected: string; onChange: (name: string) => void;
}) {
  const current = playbooks.find((playbook) => playbook.name === selected);
  return <label>Playbook
    <select name="playbook" value={selected} onChange={(event) => onChange(event.target.value)}>
      <option value="">No playbook</option>
      {playbooks.map((playbook) => <option key={playbook.name} value={playbook.name}>
        {playbook.name}{playbook.builtin ? "" : " (custom)"}
      </option>)}
    </select>
    {current && <span className="meta">{playbookSummary(current)}</span>}
  </label>;
}

function ruleText(rules: (PlaybookRequiredTask | PlaybookCriteriaRule)[] | null) {
  return (rules ?? []).map((rule) => {
    const head = [rule.name, rule.role, rule.severity === "error" ? "" : rule.severity].filter(Boolean).join(" ");
    return `${head}: ${rule.pattern}`;
  }).join("\n");
}

function parseRules(text: string) {
  return text.split("\n").map((line) => line.trim()).filter(Boolean).map((line) => {
    const split = line.indexOf(":");
    if (split < 1) throw new Error(`Rule "${line}" needs the form name [role] [severity]: pattern.`);
    const [name, ...flags] = line.slice(0, split).trim().split(/\s+/);
    const rule = { name, pattern: line.slice(split + 1).trim(), role: "", severity: "error" };
    for (const flag of flags) {
      if (roles.includes(flag)) rule.role = flag;
      else if (severities.includes(flag)) rule.severity = flag;
      else throw new Error(`Rule ${name}: unknown flag "${flag}" (use worker, tester, error or warning).`);
    }
    return rule;
  });
}

function playbookPath(name: string) {
  return `playbooks/${encodeURIComponent(name)}`;
}

function PlaybookForm({ playbook, done, cancel }: { playbook?: Playbook; done: () => void; cancel: () => void }) {
  const form = useSubmit();
  return <form className="panel stack" data-write onSubmit={(event) => form.submit(event, async (fields) => {
    const body = {
      name: playbook?.name ?? value(fields, "name"),
      description: value(fields, "description"),
      coverage: value(fields, "coverage"),
      required_tasks: parseRules(value(fields, "required_tasks")),
      criteria_rules: parseRules(value(fields, "criteria_rules")),
      allowed_sizes: fields.getAll("allowed_sizes").map(String),
      default_task_budget: {
        max_minutes: Number(value(fields, "max_minutes") || 0),
        max_cost_eur: Number(value(fields, "max_cost_eur") || 0),
      },
      brief_cost_multiplier: Number(value(fields, "multiplier") || 0),
      cross_review: fields.get("cross_review") === "on",
      instructions: value(fields, "instructions"),
    };
    if (playbook) await api<Playbook>(playbookPath(playbook.name), "PUT", body);
    else await api<Playbook>("playbooks", "POST", body);
    done();
  })}>
    <h3>{playbook ? `Edit ${playbook.name}` : "New playbook"}</h3>
    <div className="form-grid">
      <label>Name<input name="name" pattern={"[a-z0-9][a-z0-9_\\-]*"} maxLength={63} required
        defaultValue={playbook?.name} readOnly={!!playbook} /></label>
      <label>Test coverage<select name="coverage" defaultValue={playbook?.coverage ?? ""}>
        {coverage.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
      </select></label>
      <label>Default task minutes<input name="max_minutes" type="number" min="0" defaultValue={playbook?.default_task_budget.max_minutes ?? 0} /></label>
      <label>Default task budget (EUR)<input name="max_cost_eur" type="number" min="0" step="0.01" defaultValue={playbook?.default_task_budget.max_cost_eur ?? 0} /></label>
      <label>Brief budget multiplier<input name="multiplier" type="number" min="0" max="10" step="0.05" defaultValue={playbook?.brief_cost_multiplier ?? 0} /></label>
    </div>
    <fieldset className="actions">
      <legend>Allowed task sizes (none checked = any)</legend>
      {sizes.map(([id, name]) => <label key={id} className="inline">
        <input type="checkbox" name="allowed_sizes" value={id} defaultChecked={playbook?.allowed_sizes?.includes(id)} /> {name}
      </label>)}
    </fieldset>
    <label className="inline"><input type="checkbox" name="cross_review" defaultChecked={playbook?.cross_review ?? true} /> Force cross review on briefs</label>
    <label>Description<input name="description" maxLength={500} defaultValue={playbook?.description} /></label>
    <label>Required tasks, one per line: name [role] [severity]: pattern
      <textarea name="required_tasks" rows={3} spellCheck={false} defaultValue={ruleText(playbook?.required_tasks ?? null)}
        placeholder="rollback: roll ?back|revert" /></label>
    <label>Acceptance criteria rules, one per line: name [role] [severity]: pattern
      <textarea name="criteria_rules" rows={3} spellCheck={false} defaultValue={ruleText(playbook?.criteria_rules ?? null)}
        placeholder="regression_test worker: regression" /></label>
    <label>Leader instructions<textarea name="instructions" rows={3} maxLength={2000} defaultValue={playbook?.instructions} /></label>
    <Feedback error={form.error} />
    <div className="actions">
      <button className="primary" disabled={form.busy}>{playbook ? "Save playbook" : "Create playbook"}</button>
      <button type="button" onClick={cancel}>Cancel</button>
    </div>
  </form>;
}

export function PlaybooksPanel() {
  const playbooks = usePlaybooks();
  const [editing, setEditing] = useState<string | null>(null);
  const [error, setError] = useState("");
  const list = playbooks.list;
  const current = list.find((playbook) => playbook.name === editing);

  async function remove(playbook: Playbook) {
    if (!window.confirm(`Delete playbook ${playbook.name}?`)) return;
    setError("");
    try {
      await api<void>(playbookPath(playbook.name), "DELETE");
      playbooks.refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Request failed.");
    }
  }

  async function clone(playbook: Playbook) {
    const name = window.prompt(`Name of the copy of ${playbook.name}`, `${playbook.name}-copy`);
    if (!name) return;
    setError("");
    try {
      await api<Playbook>(`${playbookPath(playbook.name)}/clone`, "POST", { name });
      playbooks.refresh();
      setEditing(name);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Request failed.");
    }
  }

  function finish() {
    setEditing(null);
    playbooks.refresh();
  }

  return <section className="stack" aria-labelledby="playbooks-heading">
    <div className="heading"><h2 id="playbooks-heading">Playbooks</h2>
      <button data-write onClick={() => setEditing(editing === "" ? null : "")}>{editing === "" ? "Close form" : "New playbook"}</button></div>
    <ResourceState error={playbooks.error} loading={playbooks.data === undefined} retry={playbooks.refresh} rows={3} />
    {editing === "" && <PlaybookForm done={finish} cancel={() => setEditing(null)} />}
    {current && <PlaybookForm key={`${current.name}:${current.updated_at}`} playbook={current} done={finish} cancel={() => setEditing(null)} />}
    <Feedback error={error} />
    {playbooks.data && !list.length && <EmptyState>No playbooks.</EmptyState>}
    {!!list.length && <div className="table-wrap">
      <table>
        <thead><tr><th>Playbook</th><th>Rules</th><th>Defaults</th><th>Actions</th></tr></thead>
        <tbody>{list.map((playbook) => (
          <tr key={playbook.name}>
            <th scope="row">{playbook.name} {playbook.builtin && <span className="pill">built-in</span>}
              <div className="meta">{playbook.description}</div></th>
            <td>{playbook.coverage || "no test requirement"}
              <div className="meta">{playbook.required_tasks?.length ?? 0} required tasks · {playbook.criteria_rules?.length ?? 0} criteria rules
                · sizes {playbook.allowed_sizes?.join(", ") || "any"}</div></td>
            <td>{playbook.default_task_budget.max_minutes} min · {playbook.default_task_budget.max_cost_eur} EUR
              <div className="meta">brief budget ×{playbook.brief_cost_multiplier || 1} · cross review {playbook.cross_review ? "forced" : "free"}</div></td>
            <td><div className="actions" data-write>
              <button onClick={() => setEditing(playbook.name)}>Edit</button>
              <button onClick={() => clone(playbook)}>Clone</button>
              {!playbook.builtin && <button className="danger" onClick={() => remove(playbook)}>Delete</button>}
            </div></td>
          </tr>
        ))}</tbody>
      </table>
    </div>}
  </section>;
}
