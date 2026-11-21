"use client";

import { money, useResource } from "../../../../lib/client";
import type { Plan, Task } from "../../../../lib/types";
import { estimateMinutes } from "../../../../lib/duration";
import { EmptyState, ResourceState } from "../shared/states";

interface Stat { median: number; p80: number }
interface Range { low: number; expected: number; high: number }

interface TaskEstimate {
  task_id: string;
  estimate: { sufficient: boolean; confidence: string; minutes: Stat; cost_eur: Stat };
  exceeds_budget: string[] | null;
}

interface PlanEstimates {
  sufficient: boolean;
  history: number;
  tasks: TaskEstimate[] | null;
  totals: { tasks: number; estimated: number; minutes: Range; critical_path_minutes: Range; cost_eur: Range };
}

function fingerprint(tasks: Task[]) {
  let hash = 0;
  for (const char of JSON.stringify(tasks)) hash = (hash * 31 + char.charCodeAt(0)) | 0;
  return hash;
}

function warnings(task: Task, row: TaskEstimate | undefined) {
  const out: string[] = [];
  if (!row?.exceeds_budget) return out;
  if (row.exceeds_budget.includes("max_cost_eur")) out.push(`Expected cost ${money(row.estimate.cost_eur.median)} exceeds the ${money(task.budget.max_cost_eur)} budget`);
  if (row.exceeds_budget.includes("max_minutes")) out.push(`Expected time ${estimateMinutes(row.estimate.minutes.median)} exceeds the ${estimateMinutes(task.budget.max_minutes)} budget`);
  return out;
}

export function PlanEstimatesPanel({ plan, tasks }: { plan: Plan; tasks: Task[] }) {
  const estimates = useResource<PlanEstimates>(`plans/${encodeURIComponent(plan.id)}/estimates`, fingerprint(tasks));
  const data = estimates.data;
  const rows = new Map((data?.tasks ?? []).map((row) => [row.task_id, row]));
  const over = tasks.filter((task) => warnings(task, rows.get(task.id)).length > 0).length;
  return <section className="stack" aria-label="Expected cost and duration">
    <h3>Expected cost and duration</h3>
    <ResourceState error={estimates.error} loading={!data && !estimates.error} retry={estimates.refresh} />
    {data && !data.sufficient && <EmptyState>Not enough completed tasks to estimate yet ({data.history} so far).</EmptyState>}
    {data?.sufficient && <>
      {over > 0 && <p className="pill stale" role="status">{over} {over === 1 ? "task is" : "tasks are"} expected to exceed {over === 1 ? "its" : "their"} budget</p>}
      <div className="table-wrap">
        <table>
          <thead><tr><th>Task</th><th>Expected time</th><th>Expected cost</th><th>Confidence</th><th>Budget</th></tr></thead>
          <tbody>{tasks.map((task) => {
            const row = rows.get(task.id);
            const warned = warnings(task, row);
            return <tr key={task.id}>
              <th scope="row">{task.title}<div className="meta">{task.id}</div></th>
              <td>{row?.estimate.sufficient ? <>{estimateMinutes(row.estimate.minutes.median)}<div className="meta">up to {estimateMinutes(row.estimate.minutes.p80)}</div></> : "—"}</td>
              <td>{row?.estimate.sufficient ? <>{money(row.estimate.cost_eur.median)}<div className="meta">up to {money(row.estimate.cost_eur.p80)}</div></> : "—"}</td>
              <td>{row?.estimate.confidence ?? "none"}</td>
              <td>{warned.length ? warned.map((text) => <div key={text}><span className="pill stale">{text}</span></div>) : <span className="meta">Within budget</span>}</td>
            </tr>;
          })}</tbody>
          <tfoot><tr>
            <th scope="row">Total{data.totals.estimated < data.totals.tasks && <div className="meta">{data.totals.estimated} of {data.totals.tasks} tasks estimated</div>}</th>
            <td>{estimateMinutes(data.totals.minutes.expected)}<div className="meta">{estimateMinutes(data.totals.minutes.low)} – {estimateMinutes(data.totals.minutes.high)} · critical path {estimateMinutes(data.totals.critical_path_minutes.expected)}</div></td>
            <td>{money(data.totals.cost_eur.expected)}<div className="meta">{money(data.totals.cost_eur.low)} – {money(data.totals.cost_eur.high)}</div></td>
            <td colSpan={2} className="meta">Learned from {data.history} completed tasks</td>
          </tr></tfoot>
        </table>
      </div>
    </>}
  </section>;
}
