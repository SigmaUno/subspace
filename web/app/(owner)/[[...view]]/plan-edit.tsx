"use client";

import { useState } from "react";
import { api, money, useSubmit, value } from "../../../lib/client";
import type { Plan, Task } from "../../../lib/types";
import { Feedback } from "./components";
import { PlanEstimatesPanel } from "./plan-estimates";

const liveTasks = (plan: Plan) => plan.tasks?.filter((task) => task.status !== "cancelled") ?? [];

function lines(text: string) {
  return text.split("\n").map((line) => line.trim()).filter(Boolean);
}

function taskBody(fields: FormData) {
  return {
    title: value(fields, "title"),
    repo: value(fields, "repo"),
    role: value(fields, "role"),
    spec: value(fields, "spec"),
    acceptance_criteria: lines(String(fields.get("acceptance_criteria") ?? "")),
    depends_on: fields.getAll("depends_on").map(String),
    budget: { max_minutes: Number(value(fields, "max_minutes") || 0), max_cost_eur: Number(value(fields, "max_cost_eur") || 0) },
  };
}

function TaskFields({ task, others, repo }: { task?: Task; others: Task[]; repo: string }) {
  return <>
    <div className="form-grid">
      <label>Title<input name="title" defaultValue={task?.title} required /></label>
      <label>Repository<input name="repo" defaultValue={task?.repo ?? repo} required /></label>
      <label>Role<select name="role" defaultValue={task?.role ?? "worker"}>
        <option value="worker">Worker</option><option value="tester">Tester</option>
      </select></label>
      <label>Max minutes<input name="max_minutes" type="number" min="0" defaultValue={task?.budget.max_minutes ?? 0} /></label>
      <label>Max cost (EUR)<input name="max_cost_eur" type="number" min="0" step="any" defaultValue={task?.budget.max_cost_eur ?? 0} /></label>
    </div>
    <label>Spec<textarea name="spec" rows={4} defaultValue={task?.spec} required /></label>
    <label>Acceptance criteria, one per line<textarea name="acceptance_criteria" rows={3} defaultValue={task?.acceptance_criteria?.join("\n")} /></label>
    {!!others.length && <fieldset className="stack">
      <legend className="meta">Depends on</legend>
      {others.map((other) => <label className="inline" key={other.id}>
        <input type="checkbox" name="depends_on" value={other.id} defaultChecked={task?.depends_on?.includes(other.id)} />
        {other.id} · {other.title}
      </label>)}
    </fieldset>}
  </>;
}

function PlanTask({ task, plan, changed }: { task: Task; plan: Plan; changed: () => void }) {
  const [editing, setEditing] = useState(false);
  const form = useSubmit();
  const remove = useSubmit();
  const others = liveTasks(plan).filter((other) => other.id !== task.id);
  if (editing) {
    return <form className="stack" onSubmit={(event) => form.submit(event, async (fields) => {
      await api<Task>(`tasks/${encodeURIComponent(task.id)}`, "PATCH", taskBody(fields));
      setEditing(false);
      changed();
    })}>
      <TaskFields task={task} others={others} repo={task.repo} />
      <Feedback error={form.error} />
      <div className="actions">
        <button className="primary" disabled={form.busy}>Save {task.id}</button>
        <button type="button" onClick={() => setEditing(false)} disabled={form.busy}>Cancel</button>
      </div>
    </form>;
  }
  return <details><summary>{task.title} <span className="meta">{task.id} · {task.role}</span></summary>
    <p className="meta">{task.repo} · {money(task.budget.max_cost_eur)} · {task.budget.max_minutes} min</p>
    <p className="preserve">{task.spec}</p>
    {!!task.acceptance_criteria?.length && <ul>{task.acceptance_criteria.map((criterion, index) => <li key={index}>{criterion}</li>)}</ul>}
    {!!task.depends_on?.length && <p className="meta">Depends on {task.depends_on.join(", ")}</p>}
    <form className="stack" data-write onSubmit={(event) => remove.submit(event, async () => {
      await api<Plan>(`plans/${encodeURIComponent(plan.id)}/tasks/${encodeURIComponent(task.id)}`, "DELETE");
      changed();
    })}>
      <Feedback error={remove.error} />
      <div className="actions">
        <button type="button" onClick={() => setEditing(true)}>Edit</button>
        <button className="danger" disabled={remove.busy}>Remove from plan</button>
      </div>
    </form>
  </details>;
}

function AddTask({ plan, changed }: { plan: Plan; changed: () => void }) {
  const [open, setOpen] = useState(false);
  const form = useSubmit();
  const tasks = liveTasks(plan);
  if (!open) return <div data-write><button onClick={() => setOpen(true)}>Add task</button></div>;
  return <form className="stack" data-write onSubmit={(event) => form.submit(event, async (fields) => {
    await api<Plan>(`plans/${encodeURIComponent(plan.id)}/tasks`, "POST", taskBody(fields));
    setOpen(false);
    changed();
  })}>
    <h3>New task</h3>
    <TaskFields others={tasks} repo={tasks[0]?.repo ?? ""} />
    <Feedback error={form.error} />
    <div className="actions">
      <button className="primary" disabled={form.busy}>Add to plan</button>
      <button type="button" onClick={() => setOpen(false)} disabled={form.busy}>Cancel</button>
    </div>
  </form>;
}

function waves(tasks: Task[]) {
  const ids = new Set(tasks.map((task) => task.id));
  const done = new Set<string>();
  const out: Task[][] = [];
  let remaining = tasks;
  while (remaining.length) {
    const next = remaining.filter((task) => (task.depends_on ?? []).every((id) => done.has(id) || !ids.has(id)));
    if (!next.length) return { waves: out, stuck: remaining };
    out.push(next);
    next.forEach((task) => done.add(task.id));
    remaining = remaining.filter((task) => !done.has(task.id));
  }
  return { waves: out, stuck: [] as Task[] };
}

function DependencyPreview({ tasks }: { tasks: Task[] }) {
  const { waves: order, stuck } = waves(tasks);
  const titles = new Map(tasks.map((task) => [task.id, task.title]));
  return <section className="stack" aria-label="Dependency preview">
    <h3>Dependency preview</h3>
    {!tasks.length && <p className="empty">The plan has no tasks.</p>}
    <ol className="plan-tasks">
      {order.map((wave, index) => <li key={index}>
        <strong>{index === 0 ? "Start immediately" : `After wave ${index}`}</strong>
        <ul>{wave.map((task) => <li key={task.id}>
          {task.id} · {task.title}
          {!!task.depends_on?.length && <span className="meta"> — needs {task.depends_on.map((id) => titles.has(id) ? id : `${id} (outside plan)`).join(", ")}</span>}
        </li>)}</ul>
      </li>)}
    </ol>
    {!!stuck.length && <p className="error" role="alert">Unresolved dependency cycle: {stuck.map((task) => task.id).join(", ")}</p>}
  </section>;
}

export function PlanEditor({ plan, changed }: { plan: Plan; changed: () => void }) {
  const tasks = liveTasks(plan);
  return <>
    <ol className="plan-tasks">
      {tasks.map((task) => <li key={task.id}><PlanTask task={task} plan={plan} changed={changed} /></li>)}
    </ol>
    <AddTask plan={plan} changed={changed} />
    <PlanEstimatesPanel plan={plan} tasks={tasks} />
    <DependencyPreview tasks={tasks} />
  </>;
}
