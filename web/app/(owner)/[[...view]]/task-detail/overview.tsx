"use client";

import type { ReactNode } from "react";
import { money, safeURL } from "../../../../lib/client";
import type { TaskDetail } from "../../../../lib/task-detail-types";
import { PRBadges, usePR } from "../pr-status";
import { stamp } from "../shared/format";

function Fact({ name, children }: { name: string; children: ReactNode }) {
  return <div><div className="meta">{name}</div><div className="break">{children}</div></div>;
}

function Burn({ name, used, max, text }: { name: string; used: number; max: number; text: string }) {
  return <div className="stack burn">
    <div className="meta">{name}</div>
    <div>{text}</div>
    {max > 0 && <progress aria-label={name} value={Math.min(used, max)} max={max} />}
  </div>;
}

export function TaskOverview({ task, prStatus }: { task: TaskDetail; prStatus: ReturnType<typeof usePR> }) {
  const pr = safeURL(task.pr_url);
  return <section className="panel stack">
    <h2>Overview</h2>
    <div className="form-grid">
      <Fact name="Assignee">{task.assignee ?? "Unassigned"}</Fact>
      <Fact name="Reviewer">{task.reviewer ?? "None"}</Fact>
      <Fact name="Review rounds">{task.review_rounds}</Fact>
      <Fact name="Repository">{task.repo}</Fact>
      <Fact name="Branch">{task.branch || "—"}</Fact>
      <Fact name="Pull request">{prStatus.data
        ? <PRBadges pr={prStatus.data} />
        : pr ? <a href={pr} target="_blank" rel="noreferrer">Open pull request</a> : "No PR"}</Fact>
      <Fact name="Tests">{task.tests
        ? <>{task.tests.passed} passed · {task.tests.failed} failed{task.tests.reported_at && <> · <time dateTime={task.tests.reported_at}>{stamp(task.tests.reported_at)}</time></>}</>
        : "Not reported"}</Fact>
      <Fact name="Progress">{task.progress_pct}%</Fact>
      <Fact name="Updated"><time dateTime={task.updated_at}>{stamp(task.updated_at)}</time></Fact>
    </div>
    <div className="form-grid">
      <Burn name="Cost" used={task.cost_used} max={task.budget.max_cost_eur}
        text={`${money(task.cost_used)} / ${money(task.budget.max_cost_eur)}`} />
      <Burn name="Minutes" used={task.minutes_used} max={task.budget.max_minutes}
        text={`${task.minutes_used} / ${task.budget.max_minutes} min`} />
    </div>
  </section>;
}
