import { reportDuration } from "./duration.ts";
import type { BriefReport, TaskReport } from "./report-types";

const closed = new Set(["done", "cancelled"]);

export function entries(values: Record<string, number> | null) {
  return Object.entries(values ?? {}).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
}

export function markdown(report: BriefReport) {
  const lines = [
    `# ${report.brief_id} — ${report.title}`,
    "",
    `Generated ${report.generated_at}`,
    "",
    "## Tasks",
    "",
    "| Task | Title | Status | Agent | Queue | Work | Review | Blocked | Rounds | Cost € |",
    "|---|---|---|---|--:|--:|--:|--:|--:|--:|",
    ...(report.tasks ?? []).map((task) => `| ${task.id} | ${task.title.replaceAll("|", "\\|")} | ${task.status} | ${task.assignee || "unassigned"} | ` +
      `${reportDuration(task.queue_seconds)} | ${reportDuration(task.work_seconds)} | ${reportDuration(task.review_seconds)} | ${reportDuration(task.blocked_seconds)} | ` +
      `${task.review_rounds} | ${task.cost_eur.toFixed(2)} |`),
    "",
    "## Cost",
    "",
    `Total: ${report.cost.total_eur.toFixed(2)} €`,
    "",
    ...entries(report.cost.by_provider).map(([name, amount]) => `- ${name}: ${amount.toFixed(2)} €`),
    "",
    "## Flow",
    "",
    `- Blocked time: ${reportDuration(report.blocked_seconds)}`,
    `- Done: ${report.throughput.done} in ${report.throughput.window_hours.toFixed(2)} h (${report.throughput.per_hour.toFixed(2)} per hour)`,
  ];
  const path = report.critical_path.task_ids ?? [];
  if (path.length) lines.push(`- Critical path: ${path.join(" → ")} (${reportDuration(report.critical_path.seconds)})`);
  return lines.join("\n") + "\n";
}

export type Segment = { status: string; from: number; to: number };

export function segments(task: TaskReport, generated: number): Segment[] {
  const out: Segment[] = [];
  const steps = task.timeline ?? [];
  let status = steps[0]?.from ?? task.status;
  let since = Date.parse(task.created_at);
  for (const step of steps) {
    const at = Date.parse(step.at);
    out.push({ status, from: since, to: at });
    status = step.to;
    since = at;
  }
  if (!closed.has(status)) out.push({ status, from: since, to: Math.max(since, task.done_at ? Date.parse(task.done_at) : generated) });
  return out.filter((segment) => segment.to > segment.from);
}
