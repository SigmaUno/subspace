"use client";

import type { ReactNode } from "react";
import { api, value, useSubmit } from "../../../../lib/client";
import type { TaskDetail } from "../../../../lib/task-detail-types";
import { Feedback } from "../shared/components";
import { useConfirm, useToast } from "../shared/ui";

const settled = ["done", "cancelled"];
const reopenable = ["needs_owner", "blocked", "cancelled"];
const completable = ["approved", "needs_owner"];

function ActionForm({ title, button, danger, success, warning, children, run }: {
  title: string; button: string; danger?: boolean; success: string; warning?: string; children?: ReactNode;
  run: (fields: FormData) => Promise<void>;
}) {
  const form = useSubmit();
  const confirm = useConfirm();
  const toast = useToast();
  return <form className="stack" onSubmit={(event) => form.submit(event, async (fields, element) => {
    if (warning && !await confirm({ title: `${button}?`, message: warning, confirm: button })) return;
    await run(fields);
    toast(success);
    element.reset();
  })}>
    <h3>{title}</h3>
    {children}
    <Feedback error={form.error} />
    <div><button className={danger ? "danger" : "primary"} disabled={form.busy}>{button}</button></div>
  </form>;
}

export function OwnerActions({ task, changed }: { task: TaskDetail; changed: (task: TaskDetail) => void }) {
  const path = `tasks/${encodeURIComponent(task.id)}`;
  const transition = async (action: string, body: Record<string, string>) => {
    changed(await api<TaskDetail>(`${path}/${action}`, "POST", body));
  };
  return <section className="panel stack" data-write>
    <h2>Owner actions</h2>
    <div className="form-grid">
      {completable.includes(task.status) && <ActionForm title="Mark done" button="Mark done" success="Marked done."
        run={(fields) => transition("done", { note: value(fields, "note") })}>
        <label>Note<input name="note" placeholder="Merged in #123" /></label>
      </ActionForm>}
      {reopenable.includes(task.status) && <ActionForm title="Reopen" button="Reopen" success="Reopened."
        run={(fields) => transition("reopen", { status: value(fields, "status"), note: value(fields, "note") })}>
        <label>Status
          <select name="status" defaultValue={task.assignee ? "assigned" : "ready"}>
            <option value="ready">Ready</option>
            <option value="assigned">Assigned</option>
          </select>
        </label>
        <label>Note<input name="note" /></label>
      </ActionForm>}
      <ActionForm title="Set budget" button="Save budget" success="Budget saved." run={async (fields) => {
        const updated = await api<TaskDetail>(path, "PATCH", {
          budget: { max_minutes: Number(value(fields, "minutes")), max_cost_eur: Number(value(fields, "cost")) },
        });
        changed(updated);
      }}>
        <div className="form-grid" key={`${task.budget.max_minutes}-${task.budget.max_cost_eur}`}>
          <label>Max minutes<input name="minutes" type="number" min="0" step="1" defaultValue={task.budget.max_minutes} required /></label>
          <label>Max cost (EUR)<input name="cost" type="number" min="0" step="0.01" defaultValue={task.budget.max_cost_eur} required /></label>
        </div>
      </ActionForm>
      {!settled.includes(task.status) && <ActionForm title="Cancel task" button="Cancel task" danger success={`Cancelled ${task.id}.`}
        warning={`${task.id} is cancelled and its lease is released.`}
        run={(fields) => transition("cancel", { reason: value(fields, "reason") })}>
        <label>Reason<input name="reason" /></label>
      </ActionForm>}
    </div>
  </section>;
}
