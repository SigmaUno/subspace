"use client";

import { useEffect, useState } from "react";
import { api, useResource, useSubmit, value } from "../../../lib/client";
import type { Preview, Run, RunList, Schedule, ScheduleList, TemplateList, TemplateSummary } from "../../../lib/schedule-types";
import type { Brief } from "../../../lib/types";
import { Feedback } from "./components";
import { EmptyState, PageHeading, ResourceState } from "./states";
import { useAction, useConfirm, useToast } from "./ui";

function inZone(iso: string, zone: string) {
  try {
    return new Date(iso).toLocaleString([], { timeZone: zone, dateStyle: "medium", timeStyle: "short", timeZoneName: "short" });
  } catch {
    return new Date(iso).toLocaleString();
  }
}

function formatTime(iso: string | null, zone: string) {
  return iso ? <time dateTime={iso}>{inZone(iso, zone)}</time> : "—";
}

function NextRuns({ cron, timezone }: { cron: string; timezone: string }) {
  const [query, setQuery] = useState({ cron, timezone });
  useEffect(() => {
    const timer = window.setTimeout(() => setQuery({ cron, timezone }), 400);
    return () => window.clearTimeout(timer);
  }, [cron, timezone]);
  const zone = query.timezone.trim() || "UTC";
  const path = query.cron.trim()
    ? `schedules/preview?${new URLSearchParams({ cron: query.cron, timezone: zone, count: "5" })}`
    : null;
  const preview = useResource<Preview>(path, 0);
  if (!path) return null;
  if (preview.error) return <p className="error" role="alert">{preview.error}</p>;
  const data = preview.data;
  if (!data) return null;
  return <div className="stack" role="status" aria-label="Next runs">
    <span className="meta">Next runs in {data.timezone}</span>
    <ol>{data.next.map((iso) => <li key={iso}>{formatTime(iso, data.timezone)} <span className="meta" style={{ whiteSpace: "nowrap" }}>{iso}</span></li>)}</ol>
  </div>;
}

function Variables({ template }: { template?: TemplateSummary }) {
  if (!template?.variables.length) return null;
  return <div className="form-grid">
    {template.variables.map((name) => <label key={name}>{name}<input name={`var:${name}`} required /></label>)}
  </div>;
}

function templateVars(template: TemplateSummary | undefined, fields: FormData) {
  return Object.fromEntries((template?.variables ?? []).map((name) => [name, value(fields, `var:${name}`)]));
}

function ScheduleForm({ done }: { done: () => void }) {
  const [cron, setCron] = useState("0 9 * * 1");
  const [timezone, setTimezone] = useState("UTC");
  const [action, setAction] = useState("create_brief");
  const [source, setSource] = useState("template");
  const [briefTemplate, setBriefTemplate] = useState("");
  const [taskTemplate, setTaskTemplate] = useState("");
  const briefTemplates = useResource<TemplateList>("brief-templates", 0);
  const taskTemplates = useResource<TemplateList>("task-templates", 0);
  const briefs = useResource<{ briefs: Brief[] | null }>("briefs?status=open&limit=1000", 0);
  const form = useSubmit();
  const toast = useToast();
  const briefOptions = briefTemplates.data?.templates ?? [];
  const taskOptions = taskTemplates.data?.templates ?? [];
  const selectedBrief = briefOptions.find((template) => template.id === briefTemplate) ?? briefOptions[0];
  const selectedTask = taskOptions.find((template) => template.id === taskTemplate) ?? taskOptions[0];

  return <form className="panel stack" data-write onSubmit={(event) => form.submit(event, async (fields, element) => {
    const body: Record<string, unknown> = {
      name: value(fields, "name"), cron, timezone: timezone.trim() || "UTC", action,
      max_active: Number(value(fields, "max_active") || 1), catch_up: value(fields, "catch_up"),
    };
    if (action === "create_brief") {
      body.brief = source === "template"
        ? { template_id: selectedBrief?.id ?? "", vars: templateVars(selectedBrief, fields) }
        : { markdown: String(fields.get("markdown") ?? "") };
    } else {
      body.brief_id = value(fields, "brief_id");
      body.tasks = [{
        template_id: selectedTask?.id ?? "", vars: templateVars(selectedTask, fields),
        repo: value(fields, "repo"), priority: Number(value(fields, "priority") || 0),
      }];
    }
    const created = await api<Schedule>("schedules", "POST", body);
    toast(`Created schedule ${created.name}.`);
    element.reset();
    done();
  })}>
    <h3>New schedule</h3>
    <div className="form-grid">
      <label>Name<input name="name" maxLength={100} required /></label>
      <label>Cron<input value={cron} required placeholder="0 9 * * 1" spellCheck={false}
        onChange={(event) => setCron(event.target.value)} /></label>
      <label>Time zone<input value={timezone} placeholder="UTC" spellCheck={false}
        onChange={(event) => setTimezone(event.target.value)} /></label>
      <label>Action<select value={action} onChange={(event) => setAction(event.target.value)}>
        <option value="create_brief">Create a brief</option>
        <option value="add_tasks">Add tasks to a brief</option>
      </select></label>
      <label>Max active runs<input name="max_active" type="number" min="0" max="100" defaultValue={1} /></label>
      <label>Missed runs<select name="catch_up" defaultValue="skip">
        <option value="skip">Skip them</option>
        <option value="run-once">Run once on catch-up</option>
      </select></label>
    </div>
    <NextRuns cron={cron} timezone={timezone} />
    {action === "create_brief" && <>
      <label>Brief source<select value={source} onChange={(event) => setSource(event.target.value)}>
        <option value="template">Brief template</option>
        <option value="markdown">Import document</option>
      </select></label>
      {source === "template" && <>
        <label>Template<select value={selectedBrief?.id ?? ""} onChange={(event) => setBriefTemplate(event.target.value)}>
          {briefOptions.map((template) => <option key={template.id} value={template.id}>{template.title}</option>)}
        </select></label>
        <Variables key={selectedBrief?.id} template={selectedBrief} />
      </>}
      {source === "markdown" && <label>Brief document<textarea name="markdown" rows={10} required spellCheck={false} /></label>}
    </>}
    {action === "add_tasks" && <>
      <div className="form-grid">
        <label>Brief<select name="brief_id" required defaultValue="">
          <option value="" disabled>Choose a brief</option>
          {(briefs.data?.briefs ?? []).map((brief) => <option key={brief.id} value={brief.id}>{brief.id} · {brief.title}</option>)}
        </select></label>
        <label>Repository<input name="repo" placeholder="owner/name" required /></label>
        <label>Priority<input name="priority" type="number" defaultValue={0} /></label>
        <label>Task template<select value={selectedTask?.id ?? ""} onChange={(event) => setTaskTemplate(event.target.value)}>
          {taskOptions.map((template) => <option key={template.id} value={template.id}>{template.title}</option>)}
        </select></label>
      </div>
      <Variables key={selectedTask?.id} template={selectedTask} />
    </>}
    <Feedback error={form.error} />
    <div className="actions"><button className="primary" disabled={form.busy}>Create schedule</button></div>
  </form>;
}

function runTarget(run: Run) {
  if (run.task_ids.length) return `${run.brief_id} · ${run.task_ids.join(", ")}`;
  return run.brief_id || "—";
}

function RunsTable({ schedule }: { schedule: Schedule }) {
  const runs = useResource<RunList>(`schedules/${encodeURIComponent(schedule.id)}/runs?limit=20`, 0);
  const rows = runs.data?.runs ?? [];
  return <div className="stack">
    <ResourceState error={runs.error} loading={runs.data === undefined} retry={runs.refresh} rows={2} />
    {runs.data && !rows.length && <EmptyState>No runs yet.</EmptyState>}
    {!!rows.length && <div className="table-wrap" role="region" aria-label="Schedules" tabIndex={0}>
      <table>
        <thead><tr><th>Scheduled for</th><th>Status</th><th>Result</th><th>Details</th></tr></thead>
        <tbody>{rows.map((run) => <tr key={run.scheduled_for}>
          <th scope="row">{formatTime(run.scheduled_for, schedule.timezone)}</th>
          <td><span className={`pill ${run.status === "created" ? "active" : run.status === "failed" || run.status === "interrupted" ? "revoked" : ""}`}>{run.status}</span></td>
          <td>{runTarget(run)}</td>
          <td>{[run.detail, run.missed ? `${run.missed} missed` : ""].filter(Boolean).join(" · ") || "—"}</td>
        </tr>)}</tbody>
      </table>
    </div>}
  </div>;
}

function describe(schedule: Schedule) {
  if (schedule.action === "add_tasks") {
    const count = schedule.tasks.length;
    return `Adds ${count} ${count === 1 ? "task" : "tasks"} to ${schedule.brief_id}`;
  }
  return schedule.brief?.template_id ? `Creates a brief from ${schedule.brief.template_id}` : "Creates a brief from an import document";
}

function ScheduleItem({ schedule, refresh }: { schedule: Schedule; refresh: () => void }) {
  const [showRuns, setShowRuns] = useState(false);
  const action = useAction();
  const confirm = useConfirm();
  const busy = action.busy !== "";

  async function change(key: string, run: () => Promise<unknown>, success: string) {
    if (await action.run(key, run, success)) refresh();
  }

  async function remove() {
    const accepted = await confirm({
      title: `Delete schedule ${schedule.name}?`,
      message: "It stops creating briefs or tasks. Runs already created stay.",
      confirm: "Delete",
    });
    if (accepted) await change("delete", () => api<void>(path, "DELETE"), `Deleted schedule ${schedule.name}.`);
  }

  const path = `schedules/${encodeURIComponent(schedule.id)}`;
  return <article className="stack">
    <div className="heading">
      <div><strong>{schedule.name}</strong> <span className="meta">{schedule.id}</span></div>
      <span className={`pill ${schedule.enabled ? "active" : ""}`}>{schedule.enabled ? "enabled" : "disabled"}</span>
      <div className="actions">
        <button type="button" data-write disabled={busy}
          onClick={() => change("toggle", () => api<Schedule>(path, "PATCH", { enabled: !schedule.enabled }),
            `${schedule.enabled ? "Disabled" : "Enabled"} schedule ${schedule.name}.`)}>{schedule.enabled ? "Disable" : "Enable"}</button>
        <button type="button" aria-expanded={showRuns} onClick={() => setShowRuns(!showRuns)}>Runs</button>
        <button type="button" className="danger" data-write disabled={busy} onClick={remove}>Delete</button>
      </div>
    </div>
    <p className="meta">
      <code>{schedule.cron}</code> · {schedule.timezone} · {describe(schedule)} · max {schedule.max_active || "unlimited"} active · missed runs: {schedule.catch_up}
    </p>
    <p className="meta">Next run {formatTime(schedule.next_run_at, schedule.timezone)} · last run {formatTime(schedule.last_run_at, schedule.timezone)}</p>
    {showRuns && <RunsTable schedule={schedule} />}
  </article>;
}

export function SchedulesView({ embedded }: { embedded?: boolean }) {
  const list = useResource<ScheduleList>("schedules", 0);
  const schedules = list.data?.schedules ?? [];
  return <>
    <PageHeading title="Schedules" embedded={embedded} />
    <p className="meta">A schedule creates a brief, or adds tasks to a brief, from templates on a cron timetable.</p>
    <ScheduleForm done={list.refresh} />
    <ResourceState error={list.error} loading={list.data === undefined} retry={list.refresh} rows={3} />
    {list.data && !schedules.length && <EmptyState>No schedules yet.</EmptyState>}
    {!!schedules.length && <section className="panel stack" aria-label="Schedules">
      {schedules.map((schedule) => <ScheduleItem key={schedule.id} schedule={schedule} refresh={list.refresh} />)}
    </section>}
  </>;
}
